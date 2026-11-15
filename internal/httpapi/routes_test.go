package httpapi

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestRouteSnapshot protects public/private boundaries as well as the route list.
func TestRouteSnapshot(t *testing.T) {
	handler, _ := newSetupTestHandler(t, slog.New(slog.NewTextHandler(io.Discard, nil)), WithSetupToken(testSetupToken))
	routes, ok := handler.(chi.Routes)
	if !ok {
		t.Fatalf("handler %T does not expose chi routes", handler)
	}
	var lines []string
	err := chi.Walk(routes, func(method, route string, _ http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		lines = append(lines, fmt.Sprintf("%s %s middleware=%d", method, route, len(middlewares)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	actual := strings.Join(lines, "\n") + "\n"
	expected, err := os.ReadFile("testdata/routes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if actual != string(expected) {
		t.Fatalf("route snapshot changed (-want +got):\nwant:\n%s\ngot:\n%s", expected, actual)
	}
}
