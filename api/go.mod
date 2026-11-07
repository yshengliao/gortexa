// Module gortexa/api owns the gortexa.ai.v1 annotations proto and its Go
// bindings. It is deliberately tiny and separate from the framework: protobuf's
// global registry is keyed on the proto file path, so gortexa/ai/v1/annotations.proto
// may be linked exactly once per binary. Projects scaffolded by `gortexa create`
// depend on this module instead of regenerating their own copy, which is what
// lets them also depend on the framework itself.
module github.com/yshengliao/gortexa/api

go 1.27.0

require google.golang.org/protobuf v1.36.12
