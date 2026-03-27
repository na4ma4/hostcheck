package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	clientTimeout = 5 * time.Second
)

var healthcheckCmd = &cobra.Command{
	Use:   "healthcheck",
	Short: "Check if the server is healthy (for Docker healthcheck)",
	RunE:  healthcheckCommand,
}

func init() {
	rootCmd.AddCommand(healthcheckCmd)

	healthcheckCmd.Flags().StringP("addr", "a", "http://localhost:8080", "Server address to check")
	_ = viper.BindPFlag("healthcheck.addr", healthcheckCmd.Flags().Lookup("addr"))
	_ = viper.BindEnv("healthcheck.addr", "HEALTHCHECK_ADDR")
}

type healthResponse struct {
	Status string `json:"status"`
}

func healthcheckCommand(cmd *cobra.Command, _ []string) error {
	addr := viper.GetString("healthcheck.addr")

	client := &http.Client{
		Timeout: clientTimeout,
	}

	var req *http.Request
	{
		var err error
		req, err = http.NewRequestWithContext(cmd.Context(), http.MethodGet, addr+"/health", nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck failed: %v\n", err)
			return fmt.Errorf("healthcheck failed: %w", err)
		}
	}

	var resp *http.Response
	{
		var err error
		resp, err = client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck failed: %v\n", err)
			return fmt.Errorf("healthcheck failed: %w", err)
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck failed: status %d\n", resp.StatusCode)
		return fmt.Errorf("healthcheck failed: status %d", resp.StatusCode)
	}

	var body []byte
	{
		var err error
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck failed: reading body: %v\n", err)
			return fmt.Errorf("healthcheck failed: reading body: %w", err)
		}
	}

	var health healthResponse
	if err := json.Unmarshal(body, &health); err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck failed: parsing body: %v\n", err)
		return fmt.Errorf("healthcheck failed: parsing body: %w", err)
	}

	if health.Status != "RUNNING" {
		fmt.Fprintf(os.Stderr, "healthcheck failed: status %q\n", health.Status)
		return fmt.Errorf("healthcheck failed: status %q", health.Status)
	}

	return nil
}
