package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	appDirName        = ".brave-search"
	defaultConfigName = "config.json"
	defaultKeyName    = "key"
	defaultCacheName  = "cache"

	defaultBaseURL  = "https://api.search.brave.com/res/v1"
	defaultTimeout  = "15s"
	defaultCacheTTL = "10m"

	defaultMaxRetries     = 3
	defaultRetryBaseDelay = time.Second
	defaultRetryMaxDelay  = 30 * time.Second
)

var version = "dev"

type paths struct {
	HomeDir    string
	ConfigDir  string
	ConfigFile string
	KeyFile    string
	CacheDir   string
}

type config struct {
	BaseURL           string `json:"base_url"`
	Timeout           string `json:"timeout"`
	CacheTTL          string `json:"cache_ttl"`
	CacheEnabled      *bool  `json:"cache_enabled,omitempty"`
	DefaultCountry    string `json:"default_country,omitempty"`
	DefaultSearchLang string `json:"default_search_lang,omitempty"`
	DefaultUILang     string `json:"default_ui_lang,omitempty"`
	DefaultSafeSearch string `json:"default_safesearch"`
	DefaultCount      int    `json:"default_count"`
	APIVersion        string `json:"api_version,omitempty"`
}

type cacheEntry struct {
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	URL       string    `json:"url"`
	Status    int       `json:"status"`
	Body      string    `json:"body"`
}

type cacheStats struct {
	CacheDir       string `json:"cache_dir"`
	Entries        int    `json:"entries"`
	ExpiredEntries int    `json:"expired_entries"`
	CorruptEntries int    `json:"corrupt_entries"`
	Bytes          int64  `json:"bytes"`
}

type searchResponse struct {
	Web struct {
		Results []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"results"`
	} `json:"web"`
}

type apiError struct {
	StatusCode int
	Body       string
	Headers    map[string]string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("brave API returned %d: %s", e.StatusCode, truncate(e.Body, 300))
}

type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type searchOptions struct {
	APIKey     string
	APIKeyFile string
	ConfigFile string

	Query              string
	Count              int
	Offset             int
	Country            string
	SearchLang         string
	UILang             string
	SafeSearch         string
	Freshness          string
	Spellcheck         bool
	ExtraSnippets      bool
	Summary            bool
	EnableRichCallback bool
	ResultFilter       string
	Output             string
	NoCache            bool
	Refresh            bool
	CacheTTL           string
	Timeout            string
	APIVersion         string
	Goggles            stringList
	Params             stringList
	MaxRetries         int
}

type searchTarget struct {
	Command               string
	EndpointPath          string
	CountMax              int
	DefaultCount          int
	AllowedSafeSearch     map[string]bool
	SupportsExtraSnippets bool
	SupportsSummary       bool
	SupportsRichCallback  bool
	SupportsResultFilter  bool
	SupportsGoggles       bool
}

type requestRetryConfig struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	Sleep      func(time.Duration)
}

var (
	webTarget = searchTarget{
		Command:               "search",
		EndpointPath:          "/web/search",
		CountMax:              20,
		DefaultCount:          20,
		AllowedSafeSearch:     map[string]bool{"off": true, "moderate": true, "strict": true},
		SupportsExtraSnippets: true,
		SupportsSummary:       true,
		SupportsRichCallback:  true,
		SupportsResultFilter:  true,
		SupportsGoggles:       true,
	}
	newsTarget = searchTarget{
		Command:               "news",
		EndpointPath:          "/news/search",
		CountMax:              50,
		DefaultCount:          20,
		AllowedSafeSearch:     map[string]bool{"off": true, "moderate": true, "strict": true},
		SupportsExtraSnippets: true,
		SupportsGoggles:       true,
	}
	videosTarget = searchTarget{
		Command:           "videos",
		EndpointPath:      "/videos/search",
		CountMax:          50,
		DefaultCount:      20,
		AllowedSafeSearch: map[string]bool{"off": true, "moderate": true, "strict": true},
	}
	imagesTarget = searchTarget{
		Command:           "images",
		EndpointPath:      "/images/search",
		CountMax:          200,
		DefaultCount:      50,
		AllowedSafeSearch: map[string]bool{"off": true, "moderate": true, "strict": true},
	}
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	p, err := defaultPaths()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: determine home directory: %v\n", err)
		return 1
	}

	if len(args) == 0 {
		printRootUsage(p)
		return 2
	}

	cmd := args[0]
	switch cmd {
	case "help", "-h", "--help":
		printRootUsage(p)
		return 0
	case "version":
		fmt.Println(version)
		return 0
	case "search":
		if err := runSearch(args[1:], p, webTarget); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		return 0
	case "news":
		if err := runSearch(args[1:], p, newsTarget); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		return 0
	case "images":
		if err := runSearch(args[1:], p, imagesTarget); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		return 0
	case "videos":
		if err := runSearch(args[1:], p, videosTarget); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		return 0
	case "config":
		if err := runConfig(args[1:], p); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		return 0
	case "cache":
		if err := runCache(args[1:], p); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "error: unknown command %q\n\n", cmd)
		printRootUsage(p)
		return 2
	}
}

func runSearch(args []string, defaultPaths paths, target searchTarget) error {
	opts := searchOptions{
		ConfigFile: defaultPaths.ConfigFile,
		APIKeyFile: defaultPaths.KeyFile,
		Count:      -1,
		Offset:     -1,
		Output:     "json",
		Spellcheck: true,
		MaxRetries: defaultMaxRetries,
	}

	fs := flag.NewFlagSet(target.Command, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { printSearchUsage(defaultPaths, target) }

	fs.StringVar(&opts.Query, "q", "", "Search query")
	fs.StringVar(&opts.Query, "query", "", "Search query")
	fs.IntVar(&opts.Count, "count", -1, fmt.Sprintf("Number of results (1-%d)", target.CountMax))
	fs.IntVar(&opts.Offset, "offset", -1, "Pagination offset (0-9)")
	fs.StringVar(&opts.Country, "country", "", "Country code (example: US)")
	fs.StringVar(&opts.SearchLang, "search-lang", "", "Search language (example: en)")
	fs.StringVar(&opts.UILang, "ui-lang", "", "UI language (example: en-US)")
	fs.StringVar(&opts.SafeSearch, "safesearch", "", fmt.Sprintf("SafeSearch: %s", strings.Join(allowedSafeSearchValues(target), "|")))
	fs.StringVar(&opts.Freshness, "freshness", "", "Freshness window (example: pd, pw, pm, py, 2025-01-01to2025-01-31)")
	fs.BoolVar(&opts.Spellcheck, "spellcheck", true, "Enable spellcheck")
	fs.Var(&opts.Params, "param", "Raw query parameter key=value; can be repeated")
	if target.SupportsExtraSnippets {
		fs.BoolVar(&opts.ExtraSnippets, "extra-snippets", false, "Request extra snippets")
	}
	if target.SupportsSummary {
		fs.BoolVar(&opts.Summary, "summary", false, "Request summary")
	}
	if target.SupportsRichCallback {
		fs.BoolVar(&opts.EnableRichCallback, "enable-rich-callback", false, "Enable rich callback")
	}
	if target.SupportsResultFilter {
		fs.StringVar(&opts.ResultFilter, "result-filter", "", "Result filter list")
	}
	if target.SupportsGoggles {
		fs.Var(&opts.Goggles, "goggle", "Goggle URL; pass multiple times for multiple goggles")
	}

	fs.StringVar(&opts.APIKey, "api-key", "", "Brave API key (highest precedence)")
	fs.StringVar(&opts.APIKeyFile, "api-key-file", defaultPaths.KeyFile, "Path to API key file")
	fs.StringVar(&opts.ConfigFile, "config", defaultPaths.ConfigFile, "Path to config file")
	fs.StringVar(&opts.CacheTTL, "cache-ttl", "", "Override cache TTL (example: 30m)")
	fs.StringVar(&opts.Timeout, "timeout", "", "Request timeout (example: 20s)")
	fs.StringVar(&opts.APIVersion, "api-version", "", "Optional Brave API version header")
	fs.IntVar(&opts.MaxRetries, "max-retries", defaultMaxRetries, "Retry attempts on 429 before failing")
	fs.StringVar(&opts.Output, "output", "json", "Output format: json|pretty|urls|titles")
	fs.BoolVar(&opts.NoCache, "no-cache", false, "Disable cache reads and writes")
	fs.BoolVar(&opts.Refresh, "refresh", false, "Ignore cache and fetch fresh response")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if opts.Query == "" && fs.NArg() > 0 {
		opts.Query = strings.Join(fs.Args(), " ")
	}
	if strings.TrimSpace(opts.Query) == "" {
		return errors.New("missing query: use --q \"...\" or pass query as a positional argument")
	}

	opts.ConfigFile = expandHome(opts.ConfigFile, defaultPaths.HomeDir)
	opts.APIKeyFile = expandHome(opts.APIKeyFile, defaultPaths.HomeDir)

	cfg, _, err := loadConfig(opts.ConfigFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	timeout := firstNonEmpty(opts.Timeout, cfg.Timeout)
	if timeout == "" {
		timeout = defaultTimeout
	}
	timeoutDuration, err := time.ParseDuration(timeout)
	if err != nil {
		return fmt.Errorf("invalid timeout %q: %w", timeout, err)
	}

	count := opts.Count
	if count < 0 {
		count = cfg.DefaultCount
		if count <= 0 {
			count = target.DefaultCount
		}
		if count > target.CountMax {
			count = target.DefaultCount
		}
	}
	if count < 1 || count > target.CountMax {
		return fmt.Errorf("count must be between 1 and %d for %s, got %d", target.CountMax, target.Command, count)
	}

	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}
	if offset < 0 || offset > 9 {
		return fmt.Errorf("offset must be between 0 and 9, got %d", offset)
	}

	safeSearch := strings.ToLower(firstNonEmpty(opts.SafeSearch, cfg.DefaultSafeSearch))
	if safeSearch != "" && !target.AllowedSafeSearch[safeSearch] {
		return fmt.Errorf(
			"invalid safesearch value %q for %s (allowed: %s)",
			safeSearch,
			target.Command,
			strings.Join(allowedSafeSearchValues(target), ", "),
		)
	}

	apiVersion := firstNonEmpty(opts.APIVersion, cfg.APIVersion)
	if opts.MaxRetries < 0 {
		return fmt.Errorf("max-retries must be >= 0, got %d", opts.MaxRetries)
	}

	apiKey, _, err := resolveAPIKey(opts.APIKey, opts.APIKeyFile)
	if err != nil {
		return err
	}

	params := url.Values{}
	params.Set("q", opts.Query)
	params.Set("count", strconv.Itoa(count))
	params.Set("offset", strconv.Itoa(offset))
	params.Set("spellcheck", strconv.FormatBool(opts.Spellcheck))

	if v := firstNonEmpty(opts.Country, cfg.DefaultCountry); v != "" {
		params.Set("country", v)
	}
	if v := firstNonEmpty(opts.SearchLang, cfg.DefaultSearchLang); v != "" {
		params.Set("search_lang", v)
	}
	if v := firstNonEmpty(opts.UILang, cfg.DefaultUILang); v != "" {
		params.Set("ui_lang", v)
	}
	if safeSearch != "" {
		params.Set("safesearch", safeSearch)
	}
	if v := strings.TrimSpace(opts.Freshness); v != "" {
		params.Set("freshness", v)
	}
	if target.SupportsExtraSnippets && opts.ExtraSnippets {
		params.Set("extra_snippets", "true")
	}
	if target.SupportsSummary && opts.Summary {
		params.Set("summary", "true")
	}
	if target.SupportsRichCallback && opts.EnableRichCallback {
		params.Set("enable_rich_callback", "true")
	}
	if target.SupportsResultFilter {
		if v := strings.TrimSpace(opts.ResultFilter); v != "" {
			params.Set("result_filter", v)
		}
	}
	if target.SupportsGoggles {
		for _, goggle := range opts.Goggles {
			if strings.TrimSpace(goggle) == "" {
				continue
			}
			params.Add("goggles", goggle)
		}
	}
	if err := applyRawParams(params, opts.Params); err != nil {
		return err
	}

	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	endpointURL := baseURL + target.EndpointPath + "?" + params.Encode()

	cacheEnabled := cfg.CacheEnabled == nil || *cfg.CacheEnabled
	useCache := cacheEnabled && !opts.NoCache
	cacheTTL := firstNonEmpty(opts.CacheTTL, cfg.CacheTTL)
	if cacheTTL == "" {
		cacheTTL = defaultCacheTTL
	}
	cacheTTLDuration, err := time.ParseDuration(cacheTTL)
	if err != nil {
		return fmt.Errorf("invalid cache ttl %q: %w", cacheTTL, err)
	}

	if useCache {
		if err := os.MkdirAll(defaultPaths.CacheDir, 0o700); err != nil {
			return fmt.Errorf("create cache directory: %w", err)
		}
		if !opts.Refresh {
			cachedBody, hit, err := readCache(defaultPaths.CacheDir, endpointURL, apiVersion)
			if err != nil {
				return fmt.Errorf("read cache: %w", err)
			}
			if hit {
				return writeOutput(cachedBody, opts.Output, target.Command)
			}
		}
	}

	retryCfg := requestRetryConfig{
		MaxRetries: opts.MaxRetries,
		BaseDelay:  defaultRetryBaseDelay,
		MaxDelay:   defaultRetryMaxDelay,
		Sleep:      time.Sleep,
	}
	body, statusCode, headers, err := performRequest(endpointURL, apiKey, apiVersion, timeoutDuration, retryCfg)
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) {
			if apiErr.StatusCode == http.StatusTooManyRequests {
				return fmt.Errorf(
					"rate limited after %d retries (429): %s (remaining=%s reset=%s)",
					opts.MaxRetries,
					truncate(apiErr.Body, 200),
					firstNonEmpty(headers["X-Ratelimit-Remaining"], headers["X-RateLimit-Remaining"]),
					firstNonEmpty(headers["X-Ratelimit-Reset"], headers["X-RateLimit-Reset"]),
				)
			}
		}
		return err
	}

	if statusCode >= 200 && statusCode < 300 && useCache {
		if err := writeCache(defaultPaths.CacheDir, endpointURL, apiVersion, statusCode, body, cacheTTLDuration); err != nil {
			return fmt.Errorf("write cache: %w", err)
		}
	}

	return writeOutput(body, opts.Output, target.Command)
}

func allowedSafeSearchValues(target searchTarget) []string {
	order := []string{"off", "moderate", "strict"}
	out := make([]string, 0, len(target.AllowedSafeSearch))
	for _, key := range order {
		if target.AllowedSafeSearch[key] {
			out = append(out, key)
		}
	}
	return out
}

func runConfig(args []string, defaultPaths paths) error {
	if len(args) == 0 {
		printConfigUsage(defaultPaths)
		return nil
	}

	switch args[0] {
	case "help", "-h", "--help":
		printConfigUsage(defaultPaths)
		return nil
	case "show":
		return runConfigShow(args[1:], defaultPaths)
	case "init":
		return runConfigInit(args[1:], defaultPaths)
	case "get":
		return runConfigGet(args[1:], defaultPaths)
	case "set":
		return runConfigSet(args[1:], defaultPaths)
	case "paths":
		return runConfigPaths(args[1:], defaultPaths)
	default:
		return fmt.Errorf("unknown config command %q", args[0])
	}
}

func runConfigShow(args []string, defaultPaths paths) error {
	fs := flag.NewFlagSet("config show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := defaultPaths.ConfigFile
	fs.StringVar(&configPath, "config", defaultPaths.ConfigFile, "Path to config file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	configPath = expandHome(configPath, defaultPaths.HomeDir)
	cfg, exists, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	out := map[string]any{
		"config_file": configPath,
		"exists":      exists,
		"config":      cfg,
	}
	return printJSON(out)
}

func runConfigInit(args []string, defaultPaths paths) error {
	fs := flag.NewFlagSet("config init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := defaultPaths.ConfigFile
	force := false
	fs.StringVar(&configPath, "config", defaultPaths.ConfigFile, "Path to config file")
	fs.BoolVar(&force, "force", false, "Overwrite existing config file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	configPath = expandHome(configPath, defaultPaths.HomeDir)
	if _, err := os.Stat(configPath); err == nil && !force {
		return fmt.Errorf("config file already exists at %s (use --force to overwrite)", configPath)
	}
	if err := writeConfig(configPath, defaultConfig()); err != nil {
		return err
	}

	fmt.Printf("initialized config at %s\n", configPath)
	return nil
}

func runConfigGet(args []string, defaultPaths paths) error {
	fs := flag.NewFlagSet("config get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := defaultPaths.ConfigFile
	fs.StringVar(&configPath, "config", defaultPaths.ConfigFile, "Path to config file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: bravesearch config get <key> [--config path]")
	}

	configPath = expandHome(configPath, defaultPaths.HomeDir)
	cfg, _, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	key := fs.Arg(0)
	value, ok := getConfigValue(cfg, key)
	if !ok {
		return fmt.Errorf("unknown config key %q", key)
	}
	fmt.Println(value)
	return nil
}

func runConfigSet(args []string, defaultPaths paths) error {
	fs := flag.NewFlagSet("config set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := defaultPaths.ConfigFile
	fs.StringVar(&configPath, "config", defaultPaths.ConfigFile, "Path to config file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: bravesearch config set <key> <value> [--config path]")
	}

	configPath = expandHome(configPath, defaultPaths.HomeDir)
	cfg, _, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	key := fs.Arg(0)
	value := fs.Arg(1)
	if err := setConfigValue(&cfg, key, value); err != nil {
		return err
	}
	if err := writeConfig(configPath, cfg); err != nil {
		return err
	}

	fmt.Printf("updated %s in %s\n", normalizeConfigKey(key), configPath)
	return nil
}

func runConfigPaths(args []string, defaultPaths paths) error {
	fs := flag.NewFlagSet("config paths", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := defaultPaths.ConfigFile
	keyFile := defaultPaths.KeyFile
	fs.StringVar(&configPath, "config", defaultPaths.ConfigFile, "Path to config file")
	fs.StringVar(&keyFile, "api-key-file", defaultPaths.KeyFile, "Path to API key file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	configPath = expandHome(configPath, defaultPaths.HomeDir)
	keyFile = expandHome(keyFile, defaultPaths.HomeDir)
	out := map[string]string{
		"config_dir":  filepath.Dir(configPath),
		"config_file": configPath,
		"key_file":    keyFile,
		"cache_dir":   defaultPaths.CacheDir,
	}
	return printJSON(out)
}

func runCache(args []string, defaultPaths paths) error {
	if len(args) == 0 {
		printCacheUsage(defaultPaths)
		return nil
	}

	switch args[0] {
	case "help", "-h", "--help":
		printCacheUsage(defaultPaths)
		return nil
	case "stats":
		return runCacheStats(args[1:], defaultPaths)
	case "clear":
		return runCacheClear(args[1:], defaultPaths)
	case "prune":
		return runCachePrune(args[1:], defaultPaths)
	default:
		return fmt.Errorf("unknown cache command %q", args[0])
	}
}

func runCacheStats(args []string, defaultPaths paths) error {
	fs := flag.NewFlagSet("cache stats", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cacheDir := defaultPaths.CacheDir
	fs.StringVar(&cacheDir, "cache-dir", defaultPaths.CacheDir, "Path to cache directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	cacheDir = expandHome(cacheDir, defaultPaths.HomeDir)
	stats, err := collectCacheStats(cacheDir)
	if err != nil {
		return err
	}
	return printJSON(stats)
}

func runCacheClear(args []string, defaultPaths paths) error {
	fs := flag.NewFlagSet("cache clear", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cacheDir := defaultPaths.CacheDir
	fs.StringVar(&cacheDir, "cache-dir", defaultPaths.CacheDir, "Path to cache directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	cacheDir = expandHome(cacheDir, defaultPaths.HomeDir)
	removed, reclaimedBytes, err := clearCache(cacheDir)
	if err != nil {
		return err
	}

	return printJSON(map[string]any{
		"cache_dir":         cacheDir,
		"removed_entries":   removed,
		"reclaimed_bytes":   reclaimedBytes,
		"removed_all_files": true,
	})
}

func runCachePrune(args []string, defaultPaths paths) error {
	fs := flag.NewFlagSet("cache prune", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cacheDir := defaultPaths.CacheDir
	fs.StringVar(&cacheDir, "cache-dir", defaultPaths.CacheDir, "Path to cache directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	cacheDir = expandHome(cacheDir, defaultPaths.HomeDir)
	removed, reclaimedBytes, err := pruneExpiredCache(cacheDir)
	if err != nil {
		return err
	}

	return printJSON(map[string]any{
		"cache_dir":       cacheDir,
		"removed_entries": removed,
		"reclaimed_bytes": reclaimedBytes,
	})
}

func defaultPaths() (paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return paths{}, err
	}
	configDir := filepath.Join(home, appDirName)
	return paths{
		HomeDir:    home,
		ConfigDir:  configDir,
		ConfigFile: filepath.Join(configDir, defaultConfigName),
		KeyFile:    filepath.Join(configDir, defaultKeyName),
		CacheDir:   filepath.Join(configDir, defaultCacheName),
	}, nil
}

func defaultConfig() config {
	return config{
		BaseURL:           defaultBaseURL,
		Timeout:           defaultTimeout,
		CacheTTL:          defaultCacheTTL,
		CacheEnabled:      boolPtr(true),
		DefaultSafeSearch: "moderate",
		DefaultCount:      20,
	}
}

func normalizeConfig(cfg config) config {
	def := defaultConfig()
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = def.BaseURL
	}
	if strings.TrimSpace(cfg.Timeout) == "" {
		cfg.Timeout = def.Timeout
	}
	if strings.TrimSpace(cfg.CacheTTL) == "" {
		cfg.CacheTTL = def.CacheTTL
	}
	if cfg.CacheEnabled == nil {
		cfg.CacheEnabled = boolPtr(true)
	}
	if cfg.DefaultCount < 1 || cfg.DefaultCount > 200 {
		cfg.DefaultCount = def.DefaultCount
	}
	if strings.TrimSpace(cfg.DefaultSafeSearch) == "" {
		cfg.DefaultSafeSearch = def.DefaultSafeSearch
	}
	if !isValidSafeSearch(cfg.DefaultSafeSearch) {
		cfg.DefaultSafeSearch = def.DefaultSafeSearch
	}
	return cfg
}

func loadConfig(path string) (config, bool, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, false, nil
		}
		return config{}, false, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return config{}, true, fmt.Errorf("parse config %s: %w", path, err)
	}
	return normalizeConfig(cfg), true, nil
}

func writeConfig(path string, cfg config) error {
	cfg = normalizeConfig(cfg)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func resolveAPIKey(cliKey, keyFile string) (key string, source string, err error) {
	if v := strings.TrimSpace(cliKey); v != "" {
		return v, "--api-key", nil
	}

	for _, envKey := range []string{"BRAVE_API_KEY", "BRAVE_SEARCH_API_KEY"} {
		if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
			return v, envKey, nil
		}
	}

	data, readErr := os.ReadFile(keyFile)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return "", "", fmt.Errorf(
				"missing API key: use --api-key, BRAVE_API_KEY (or BRAVE_SEARCH_API_KEY), or create %s",
				keyFile,
			)
		}
		return "", "", fmt.Errorf("read API key file %s: %w", keyFile, readErr)
	}
	if v := strings.TrimSpace(string(data)); v != "" {
		return v, keyFile, nil
	}

	return "", "", fmt.Errorf("API key file %s is empty", keyFile)
}

func performRequest(
	fullURL, apiKey, apiVersion string,
	timeout time.Duration,
	retryCfg requestRetryConfig,
) ([]byte, int, map[string]string, error) {
	return performRequestWithRetry(
		func() ([]byte, int, map[string]string, error) {
			return performRequestOnce(fullURL, apiKey, apiVersion, timeout)
		},
		retryCfg,
	)
}

func performRequestWithRetry(
	requester func() ([]byte, int, map[string]string, error),
	retryCfg requestRetryConfig,
) ([]byte, int, map[string]string, error) {
	if retryCfg.BaseDelay <= 0 {
		retryCfg.BaseDelay = defaultRetryBaseDelay
	}
	if retryCfg.MaxDelay <= 0 {
		retryCfg.MaxDelay = defaultRetryMaxDelay
	}
	if retryCfg.Sleep == nil {
		retryCfg.Sleep = time.Sleep
	}

	var lastErr error
	var lastStatus int
	var lastHeaders map[string]string
	for attempt := 0; attempt <= retryCfg.MaxRetries; attempt++ {
		body, statusCode, headers, err := requester()
		if err == nil {
			return body, statusCode, headers, nil
		}

		lastErr = err
		lastStatus = statusCode
		lastHeaders = headers

		var apiErr *apiError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests || attempt == retryCfg.MaxRetries {
			return nil, lastStatus, lastHeaders, err
		}

		waitFor := retryDelayFromHeaders(headers, attempt, retryCfg.BaseDelay, retryCfg.MaxDelay)
		retryCfg.Sleep(waitFor)
	}

	return nil, lastStatus, lastHeaders, lastErr
}

func performRequestOnce(fullURL, apiKey, apiVersion string, timeout time.Duration) ([]byte, int, map[string]string, error) {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)
	if strings.TrimSpace(apiVersion) != "" {
		req.Header.Set("Api-Version", apiVersion)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, resp.StatusCode, nil, err
	}

	headerMap := buildHeaderMap(resp.Header)

	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, headerMap, &apiError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
			Headers:    headerMap,
		}
	}
	return body, resp.StatusCode, headerMap, nil
}

func buildHeaderMap(h http.Header) map[string]string {
	return map[string]string{
		"Retry-After":           h.Get("Retry-After"),
		"X-Ratelimit-Limit":     h.Get("X-Ratelimit-Limit"),
		"X-Ratelimit-Remaining": h.Get("X-Ratelimit-Remaining"),
		"X-Ratelimit-Reset":     h.Get("X-Ratelimit-Reset"),
		"X-RateLimit-Limit":     h.Get("X-RateLimit-Limit"),
		"X-RateLimit-Remaining": h.Get("X-RateLimit-Remaining"),
		"X-RateLimit-Reset":     h.Get("X-RateLimit-Reset"),
	}
}

func retryDelayFromHeaders(headers map[string]string, attempt int, baseDelay, maxDelay time.Duration) time.Duration {
	if baseDelay <= 0 {
		baseDelay = defaultRetryBaseDelay
	}
	if maxDelay <= 0 {
		maxDelay = defaultRetryMaxDelay
	}
	if attempt < 0 {
		attempt = 0
	}

	delay := baseDelay
	for i := 0; i < attempt; i++ {
		if delay >= maxDelay {
			delay = maxDelay
			break
		}
		delay *= 2
	}

	headerDelay := parseRetryAfterHeader(headers["Retry-After"])
	if headerDelay == 0 {
		headerDelay = parseRateLimitResetHeader(firstNonEmpty(headers["X-Ratelimit-Reset"], headers["X-RateLimit-Reset"]))
	}
	if headerDelay > delay {
		delay = headerDelay
	}
	if delay > maxDelay {
		delay = maxDelay
	}
	if delay <= 0 {
		delay = baseDelay
	}
	return delay
}

func parseRetryAfterHeader(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}

	if when, err := http.ParseTime(value); err == nil {
		waitFor := time.Until(when)
		if waitFor < 0 {
			return 0
		}
		return waitFor
	}
	return 0
}

func parseRateLimitResetHeader(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}

	parts := strings.Split(value, ",")
	var minSeconds int64
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		seconds, err := strconv.ParseInt(part, 10, 64)
		if err != nil || seconds <= 0 {
			continue
		}
		if minSeconds == 0 || seconds < minSeconds {
			minSeconds = seconds
		}
	}
	if minSeconds == 0 {
		return 0
	}
	return time.Duration(minSeconds) * time.Second
}

func applyRawParams(params url.Values, raw []string) error {
	overridden := map[string]bool{}
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key, value, found := strings.Cut(item, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !found || key == "" {
			return fmt.Errorf("invalid --param value %q (expected key=value)", item)
		}
		if !overridden[key] {
			params.Del(key)
			overridden[key] = true
		}
		params.Add(key, value)
	}
	return nil
}

func writeOutput(body []byte, mode, command string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "json"
	}

	var out []byte
	switch mode {
	case "json":
		out = append(bytes.TrimSpace(body), '\n')
	case "pretty":
		var buf bytes.Buffer
		if err := json.Indent(&buf, body, "", "  "); err != nil {
			return fmt.Errorf("format pretty JSON: %w", err)
		}
		out = append(buf.Bytes(), '\n')
	case "urls":
		if command != webTarget.Command {
			return fmt.Errorf("--output urls is only supported for %q", webTarget.Command)
		}
		lines, err := extractSearchField(body, "url")
		if err != nil {
			return err
		}
		out = []byte(strings.Join(lines, "\n") + "\n")
	case "titles":
		if command != webTarget.Command {
			return fmt.Errorf("--output titles is only supported for %q", webTarget.Command)
		}
		lines, err := extractSearchField(body, "title")
		if err != nil {
			return err
		}
		out = []byte(strings.Join(lines, "\n") + "\n")
	default:
		return fmt.Errorf("invalid output format %q (allowed: json, pretty, urls, titles)", mode)
	}

	_, err := os.Stdout.Write(out)
	return err
}

func extractSearchField(body []byte, field string) ([]string, error) {
	var payload searchResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse response JSON: %w", err)
	}

	out := make([]string, 0, len(payload.Web.Results))
	for _, result := range payload.Web.Results {
		switch field {
		case "url":
			if result.URL != "" {
				out = append(out, result.URL)
			}
		case "title":
			if result.Title != "" {
				out = append(out, result.Title)
			}
		default:
			return nil, fmt.Errorf("unsupported field %q", field)
		}
	}
	return out, nil
}

func buildCacheKey(fullURL, apiVersion string) string {
	raw := fullURL + "|api-version=" + apiVersion
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func cacheEntryPath(cacheDir, fullURL, apiVersion string) string {
	return filepath.Join(cacheDir, buildCacheKey(fullURL, apiVersion)+".json")
}

func readCache(cacheDir, fullURL, apiVersion string) ([]byte, bool, error) {
	path := cacheEntryPath(cacheDir, fullURL, apiVersion)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}

	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		_ = os.Remove(path)
		return nil, false, nil
	}

	if time.Now().After(entry.ExpiresAt) {
		_ = os.Remove(path)
		return nil, false, nil
	}
	return []byte(entry.Body), true, nil
}

func writeCache(cacheDir, fullURL, apiVersion string, status int, body []byte, ttl time.Duration) error {
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return err
	}

	now := time.Now().UTC()
	entry := cacheEntry{
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
		URL:       fullURL,
		Status:    status,
		Body:      string(body),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	target := cacheEntryPath(cacheDir, fullURL, apiVersion)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

func collectCacheStats(cacheDir string) (cacheStats, error) {
	stats := cacheStats{CacheDir: cacheDir}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return stats, nil
		}
		return stats, err
	}

	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(cacheDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		stats.Entries++
		stats.Bytes += info.Size()

		data, err := os.ReadFile(path)
		if err != nil {
			stats.CorruptEntries++
			continue
		}
		var item cacheEntry
		if err := json.Unmarshal(data, &item); err != nil {
			stats.CorruptEntries++
			continue
		}
		if now.After(item.ExpiresAt) {
			stats.ExpiredEntries++
		}
	}

	return stats, nil
}

func clearCache(cacheDir string) (removed int, reclaimedBytes int64, err error) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(cacheDir, entry.Name())
		info, infoErr := entry.Info()
		if infoErr == nil {
			reclaimedBytes += info.Size()
		}
		if removeErr := os.Remove(path); removeErr != nil {
			return removed, reclaimedBytes, removeErr
		}
		removed++
	}
	return removed, reclaimedBytes, nil
}

func pruneExpiredCache(cacheDir string) (removed int, reclaimedBytes int64, err error) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}

	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		path := filepath.Join(cacheDir, entry.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}

		var item cacheEntry
		if err := json.Unmarshal(data, &item); err != nil {
			continue
		}
		if now.Before(item.ExpiresAt) {
			continue
		}

		info, infoErr := entry.Info()
		if infoErr == nil {
			reclaimedBytes += info.Size()
		}
		if removeErr := os.Remove(path); removeErr != nil {
			return removed, reclaimedBytes, removeErr
		}
		removed++
	}
	return removed, reclaimedBytes, nil
}

func getConfigValue(cfg config, key string) (any, bool) {
	switch normalizeConfigKey(key) {
	case "base_url":
		return cfg.BaseURL, true
	case "timeout":
		return cfg.Timeout, true
	case "cache_ttl":
		return cfg.CacheTTL, true
	case "cache_enabled":
		if cfg.CacheEnabled == nil {
			return true, true
		}
		return *cfg.CacheEnabled, true
	case "default_country":
		return cfg.DefaultCountry, true
	case "default_search_lang":
		return cfg.DefaultSearchLang, true
	case "default_ui_lang":
		return cfg.DefaultUILang, true
	case "default_safesearch":
		return cfg.DefaultSafeSearch, true
	case "default_count":
		return cfg.DefaultCount, true
	case "api_version":
		return cfg.APIVersion, true
	default:
		return nil, false
	}
}

func setConfigValue(cfg *config, key, value string) error {
	k := normalizeConfigKey(key)
	value = strings.TrimSpace(value)

	switch k {
	case "base_url":
		if value == "" {
			return errors.New("base_url cannot be empty")
		}
		cfg.BaseURL = value
	case "timeout":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("invalid timeout value %q: %w", value, err)
		}
		cfg.Timeout = value
	case "cache_ttl":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("invalid cache_ttl value %q: %w", value, err)
		}
		cfg.CacheTTL = value
	case "cache_enabled":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid cache_enabled value %q: %w", value, err)
		}
		cfg.CacheEnabled = boolPtr(parsed)
	case "default_country":
		cfg.DefaultCountry = value
	case "default_search_lang":
		cfg.DefaultSearchLang = value
	case "default_ui_lang":
		cfg.DefaultUILang = value
	case "default_safesearch":
		if !isValidSafeSearch(value) {
			return fmt.Errorf("invalid default_safesearch %q (allowed: off, moderate, strict)", value)
		}
		cfg.DefaultSafeSearch = value
	case "default_count":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid default_count value %q: %w", value, err)
		}
		if n < 1 || n > 200 {
			return fmt.Errorf("default_count must be between 1 and 200, got %d", n)
		}
		cfg.DefaultCount = n
	case "api_version":
		cfg.APIVersion = value
	default:
		return fmt.Errorf("unsupported config key %q", key)
	}

	return nil
}

func normalizeConfigKey(key string) string {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.ReplaceAll(k, "-", "_")
	return k
}

func isValidSafeSearch(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "moderate", "strict":
		return true
	default:
		return false
	}
}

func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return path
}

func boolPtr(v bool) *bool {
	value := v
	return &value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printRootUsage(p paths) {
	fmt.Fprintf(os.Stderr, `bravesearch - Simple Brave Search CLI

Usage:
  bravesearch <command> [options]

Commands:
  search      Run Brave web search
  news        Run Brave news search
  images      Run Brave image search
  videos      Run Brave video search
  config      Manage configuration in %s
  cache       Manage cache in %s
  version     Print version
  help        Show this help

API key resolution order:
  1) --api-key
  2) BRAVE_API_KEY (or BRAVE_SEARCH_API_KEY)
  3) %s

Run "bravesearch <command> --help" for command-specific options.
`, p.ConfigFile, p.CacheDir, p.KeyFile)
}

func printSearchUsage(p paths, target searchTarget) {
	extra := ""
	if target.SupportsExtraSnippets {
		extra += "  --extra-snippets              Request extra snippets\n"
	}
	if target.SupportsSummary {
		extra += "  --summary                     Request summary\n"
	}
	if target.SupportsRichCallback {
		extra += "  --enable-rich-callback        Enable rich callback\n"
	}
	if target.SupportsResultFilter {
		extra += "  --result-filter string        Filter result types\n"
	}
	if target.SupportsGoggles {
		extra += "  --goggle value                Goggle URL (repeatable)\n"
	}

	examples := fmt.Sprintf(
		"  bravesearch %s --q \"latest golang release\"\n  bravesearch %s --q \"distributed tracing\" --count 5 --output pretty\n",
		target.Command,
		target.Command,
	)
	if target.Command == webTarget.Command {
		examples += "  bravesearch search --q \"site:go.dev context cancellation\" --output titles\n"
	}

	fmt.Fprintf(os.Stderr, `Usage:
  bravesearch %s [flags] [query]

Core flags:
  --q, --query string           Search query
  --count int                   Number of results (1-%d)
  --offset int                  Pagination offset (0-9)
  --country string              Country (example: US)
  --search-lang string          Search language (example: en)
  --ui-lang string              UI language (example: en-US)
  --safesearch string           %s
  --freshness string            pd|pw|pm|py|YYYY-MM-DDtoYYYY-MM-DD
  --spellcheck                  Enable spellcheck (default true)
%s  --param key=value             Pass raw query param (repeatable)

Auth and config:
  --api-key string              API key (highest precedence)
  --api-key-file string         API key file (default %s)
  --config string               Config file (default %s)
  --api-version string          Optional Api-Version request header
  --timeout duration            Request timeout override (example: 20s)
  --max-retries int             Retry attempts on 429 before failing (default %d)

Cache and output:
  --no-cache                    Disable cache reads/writes
  --refresh                     Ignore cache and fetch fresh response
  --cache-ttl duration          Cache TTL override (example: 30m)
  --output string               json|pretty|urls|titles (default json)
                                urls/titles are only supported for the search command

Examples:
%s`, target.Command, target.CountMax, strings.Join(allowedSafeSearchValues(target), "|"), extra, p.KeyFile, p.ConfigFile, defaultMaxRetries, examples)
}

func printConfigUsage(p paths) {
	fmt.Fprintf(os.Stderr, `Usage:
  bravesearch config <subcommand> [options]

Subcommands:
  show                 Show effective config
  init [--force]       Write default config file
  get <key>            Read a config value
  set <key> <value>    Write a config value
  paths                Show paths used by the CLI

Default config file:
  %s

Supported config keys:
  base_url, timeout, cache_ttl, cache_enabled,
  default_country, default_search_lang, default_ui_lang,
  default_safesearch, default_count, api_version
`, p.ConfigFile)
}

func printCacheUsage(p paths) {
	fmt.Fprintf(os.Stderr, `Usage:
  bravesearch cache <subcommand> [options]

Subcommands:
  stats                Show cache stats
  clear                Remove all cache entries
  prune                Remove only expired entries

Default cache directory:
  %s
`, p.CacheDir)
}
