package main

import (
	"net/http"
	"strings"
)

// rpcPathPrefix starts the path of every RPC of the API.
const rpcPathPrefix = "/openbeehive.v1."

// requireRPCContentType answers an RPC request without a Content-Type with a
// readable error. The RPC layer refuses it with an empty 415, which leaves
// someone trying the JSON API with curl guessing.
func requireRPCContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, rpcPathPrefix) && r.Header.Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Accept-Post", "application/json, application/proto")
			w.WriteHeader(http.StatusUnsupportedMediaType)
			_, _ = w.Write([]byte(`{"code":"invalid_argument","message":"Content-Type is missing: send application/json (or application/proto)"}` + "\n"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
