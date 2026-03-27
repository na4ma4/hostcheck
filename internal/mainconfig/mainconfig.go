// Package mainconfig handles configuration initialization.
package mainconfig

import (
	"errors"

	"github.com/na4ma4/config"
	"github.com/spf13/viper"
)

//nolint:gochecknoglobals // cfg is the global configuration instance.
var cfg config.Conf

// Config returns the global configuration.
func Config() config.Conf {
	return cfg
}

// ConfigInit initializes the configuration from viper.
func ConfigInit() error {
	viper.SetConfigName("hostcheck")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("/etc/hostcheck")
	viper.AddConfigPath("$HOME/.hostcheck")

	if err := viper.ReadInConfig(); err != nil {
		// Don't error on missing config file, use defaults
		var notFoundErr viper.ConfigFileNotFoundError
		if !errors.As(err, &notFoundErr) {
			return err
		}
	}

	cfg = config.NewViperConfigFromViper(viper.GetViper(), "hostcheck")

	return nil
}
