package httpapi

import "net/http"

// Direct handler tests cover native validation below the unified capability gate.
func sessionNativeHandler(api *API, engine, action string) http.HandlerFunc {
	driver, err := api.sessionDriver(engine)
	if err != nil {
		panic(err)
	}
	return driver.Handler(action)
}
