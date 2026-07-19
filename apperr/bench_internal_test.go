package apperr

import "testing"

// BenchmarkErrorResolve measures the transport-mapping hot path: the *Error
// type assertion (errors.As / errors.AsType), category lookup, and gRPC status
// construction for a mapped domain error.
func BenchmarkErrorResolve(b *testing.B) {
	err := New(CatNotFound, "resource not found")
	b.ReportAllocs()
	for b.Loop() {
		_ = Default.ToGRPCStatus(err)
	}
}
