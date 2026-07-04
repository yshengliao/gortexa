package main

import "testing"

// TestAuthSkip pins the auth-exemption surface: health is always exempt,
// reflection only under the flag, and nothing else — a widened prefix (e.g.
// "/grpc.") would silently disable auth for every gRPC-namespaced service.
func TestAuthSkip(t *testing.T) {
	cases := []struct {
		name       string
		reflection bool
		method     string
		want       bool
	}{
		{"health exempt without reflection", false, "/grpc.health.v1.Health/Check", true},
		{"health exempt with reflection", true, "/grpc.health.v1.Health/Check", true},
		{"reflection blocked when disabled", false, "/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", false},
		{"reflection exempt when enabled", true, "/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", true},
		{"v1alpha reflection exempt when enabled", true, "/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo", true},
		{"prefix cannot leak past the dot", true, "/grpc.reflectionx.Evil/Method", false},
		{"health prefix cannot leak past the dot", true, "/grpc.healthx.Evil/Method", false},
		{"domain services stay authenticated", true, "/resource.v1.ResourceService/ListResources", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := authSkip(tc.reflection)(tc.method); got != tc.want {
				t.Errorf("authSkip(%v)(%q) = %v, want %v", tc.reflection, tc.method, got, tc.want)
			}
		})
	}
}
