// Package plugin handles loading and management of check plugins.
package plugin

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"plugin"
	"strings"
	"sync"

	"github.com/na4ma4/go-slogtool"
	"github.com/na4ma4/hostcheck/pkg/check"
)

var (
	// ErrNoPluginsDirectory is returned when the plugins directory does not exist.
	ErrNoPluginsDirectory = errors.New("plugins directory does not exist")
	// ErrNoPluginsLoaded is returned when no plugins were successfully loaded.
	ErrNoPluginsLoaded = errors.New("no plugins loaded")
)

// Registry holds all loaded plugins.
type Registry struct {
	lock    sync.Mutex
	checks  map[string]check.Check
	configs map[string]map[string]any
	logger  *slog.Logger
}

// NewRegistry creates a new plugin registry.
func NewRegistry(logger *slog.Logger) *Registry {
	return &Registry{
		checks:  make(map[string]check.Check),
		configs: make(map[string]map[string]any),
		logger:  logger,
	}
}

// LoadPlugin loads a single plugin from a .so file.
func (r *Registry) LoadPlugin(path string) error {
	r.logger.Debug("opening plugin file", "path", path)

	plug, err := plugin.Open(path)
	if err != nil {
		r.logger.Error("failed to open plugin", "path", path, "error", err)
		return err
	}

	r.logger.Debug("looking up Check symbol")

	sym, err := plug.Lookup("Check")
	if err != nil {
		r.logger.Error("failed to lookup Check symbol", "path", path, "error", err)
		return err
	}

	r.logger.Debug("type asserting Check")

	r.logger.Debug("reflect type of Check", "type", fmt.Sprintf("%T", sym))

	var ck check.Check
	{
		c, ok := sym.(*check.Check)
		if !ok {
			r.logger.Error("type assertion failed", "path", path)
			return errors.New("plugin does not implement check.Check")
		}

		ck = *c
	}

	r.lock.Lock()
	defer r.lock.Unlock()

	// Store the plugin instance in the registry
	r.checks[strings.ToLower(ck.Name())] = ck
	r.logger.Info("loaded plugin", slog.String("name", ck.Name()), slog.String("path", path))

	return nil
}

// LoadDirectory loads all plugins from a directory.
// Returns ErrNoPluginsDirectory if the directory does not exist.
func (r *Registry) LoadDirectory(dir string) error {
	r.logger.Debug("loading plugins from directory", slog.String("dir", dir))

	// Check if directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrNoPluginsDirectory, dir)
	}

	var matches []string
	{
		var err error
		matches, err = filepath.Glob(filepath.Join(dir, "*.so"))
		if err != nil {
			return err
		}
	}

	r.logger.Debug("found files", slog.Int("count", len(matches)), slog.Any("files", matches))

	for _, match := range matches {
		r.logger.Debug("loading plugin", slog.String("path", match))
		if err := r.LoadPlugin(match); err != nil {
			r.logger.Error("failed to load plugin", slog.String("path", match), slogtool.ErrorAttr(err))
		}
	}

	// Check if any plugins were loaded
	if len(r.checks) == 0 {
		return ErrNoPluginsLoaded
	}

	return nil
}

// Get returns a check by name.
func (r *Registry) Get(name string) check.Check {
	r.lock.Lock()
	defer r.lock.Unlock()

	return r.checks[fmtKey(name)]
}

// GetConfig returns the per-plugin configuration from viper.
func (r *Registry) GetConfig(name string) map[string]any {
	r.lock.Lock()
	defer r.lock.Unlock()

	out := make(map[string]any, len(r.configs[fmtKey(name)]))
	maps.Copy(out, r.configs[fmtKey(name)])

	return out
}

// Configs returns all plugin configurations.
func (r *Registry) Configs() map[string]map[string]any {
	r.lock.Lock()
	defer r.lock.Unlock()

	out := make(map[string]map[string]any, len(r.configs))
	maps.Copy(out, r.configs)

	return out
}

// SetConfig sets the per-plugin configuration.
func (r *Registry) SetConfig(name string, cfg map[string]any) {
	r.lock.Lock()
	defer r.lock.Unlock()

	r.configs[fmtKey(name)] = cfg
}

// All returns all registered checks.
func (r *Registry) All() map[string]check.Check {
	r.lock.Lock()
	defer r.lock.Unlock()

	out := make(map[string]check.Check, len(r.checks))
	maps.Copy(out, r.checks)

	return out
}

// Register adds a check to the registry.
func (r *Registry) Register(c check.Check) {
	r.lock.Lock()
	defer r.lock.Unlock()

	r.checks[fmtKey(c.Name())] = c
	r.logger.Info("registered built-in check", slog.String("name", c.Name()))
}

// Names returns all registered check names.
func (r *Registry) Names() []string {
	r.lock.Lock()
	defer r.lock.Unlock()

	names := make([]string, 0, len(r.checks))
	for name := range r.checks {
		names = append(names, name)
	}

	return names
}

// Filter returns checks matching the given names (or all if empty).
func (r *Registry) Filter(names []string) []check.Check {
	r.lock.Lock()
	defer r.lock.Unlock()

	if len(names) == 0 {
		all := make([]check.Check, 0, len(r.checks))
		for _, c := range r.checks {
			all = append(all, c)
		}

		return all
	}

	result := make([]check.Check, 0, len(names))
	for _, name := range names {
		if c, ok := r.checks[fmtKey(name)]; ok {
			result = append(result, c)
		}
	}

	return result
}

func fmtKey(in string) string {
	return strings.ToLower(in)
}
