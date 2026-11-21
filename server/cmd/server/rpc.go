package main

import (
	"mime"
	"net/http"
	"strings"
)

// rpcPathPrefix starts the path of every RPC of the API.
const rpcPathPrefix = "/openbeehive.v1."

// rpcMediaTypes are the media types (or their beginnings) the RPC layer
// accepts: Connect with JSON or protobuf, gRPC and gRPC-Web.
var rpcMediaTypes = []string{
	"application/json", "application/proto", "application/connect+",
	"application/grpc",
}

// requireRPCContentType answers an RPC request without a Content-Type, or
// with one the RPC layer does not speak, with a readable error. The RPC
// layer refuses it with an empty 415, which leaves someone trying the JSON
// API with curl guessing.
func requireRPCContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, rpcPathPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		msg := ""
		switch ct := r.Header.Get("Content-Type"); {
		case ct == "":
			msg = "Content-Type is missing: send application/json (or application/proto)"
		case !rpcMediaType(ct):
			msg = "Content-Type is not supported: send application/json (or application/proto)"
		}
		if msg == "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Accept-Post", "application/json, application/proto")
		w.WriteHeader(http.StatusUnsupportedMediaType)
		_, _ = w.Write([]byte(`{"code":"invalid_argument","message":"` + msg + `"}` + "\n"))
	})
}

// rpcMediaType reports whether the RPC layer accepts the Content-Type.
func rpcMediaType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	for _, known := range rpcMediaTypes {
		if strings.HasPrefix(mt, known) {
			return true
		}
	}
	return false
}
