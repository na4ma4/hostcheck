//go:build !debug

package server

import (
	"net/http"
)

func addAdditionalMux(_ *http.ServeMux) {
}
