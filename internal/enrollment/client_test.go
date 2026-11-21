package enrollment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeviceKeyPersistsAndRejectsInsecureFiles(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "registration")
	first, err := deviceKey(directory)
	if err != nil {
		t.Fatal(err)
	}
	again, err := deviceKey(directory)
	if err != nil || first != again || len(first) != 64 {
		t.Fatal("device identity changed across retries")
	}
	if err := os.Chmod(filepath.Join(directory, "device.key"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := deviceKey(directory); err == nil {
		t.Fatal("read a world-readable device key")
	}
	if err := os.Remove(filepath.Join(directory, "device.key")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(target, []byte(first), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "device.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := deviceKey(directory); err == nil {
		t.Fatal("followed a device-key symlink")
	}
}
func TestRegistrationClientKeepsCredentialsOutOfURLAndDoesNotFollowRedirects(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	key := strings.Repeat("a", 64)
	claims := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), key) || strings.Contains(r.URL.String(), "one-time-code") {
			t.Error("credential in request URL")
		}
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("missing device authentication")
		}
		if strings.HasSuffix(r.URL.Path, "/claim") {
			claims++
			var input map[string]string
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input["code"] != "one-time-code" || input["device_key"] != key {
				t.Error("claim lost bound credentials")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"registration":{"id":"` + id + `","state":"installing"}}`))
			return
		}
		http.Redirect(w, r, "/leak-device-key", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, id, key)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := client.Claim(context.Background(), "one-time-code")
	if err != nil || plan.Registration.State != "installing" || claims != 1 {
		t.Fatalf("claim failed: %+v %v", plan, err)
	}
	if _, err := client.Session(context.Background()); err == nil {
		t.Fatal("followed installation redirect")
	}
	for _, control := range []string{"https://user:password@example.test", "https://example.test/base", "https://example.test/?token=secret"} {
		if _, err := NewClient(control, id, key); err == nil {
			t.Fatal("accepted invalid control URL")
		}
	}
}

func TestDeviceKeyCannotBeReusedAtAnotherControlURL(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "registration")
	if _, err := deviceKey(directory); err != nil {
		t.Fatal(err)
	}
	if err := bindControlURL(directory, "https://wio.example.test"); err != nil {
		t.Fatal(err)
	}
	if err := bindControlURL(directory, "https://wio.example.test"); err != nil {
		t.Fatal(err)
	}
	if err := bindControlURL(directory, "https://another.example.test"); err == nil {
		t.Fatal("device key could be sent to another origin")
	}
}
