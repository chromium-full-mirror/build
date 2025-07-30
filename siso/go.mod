module go.chromium.org/build/siso

go 1.24.5

replace go.chromium.org/build/kajiya => ../kajiya

require (
	cloud.google.com/go/compute/metadata v0.7.0
	cloud.google.com/go/logging v1.13.0
	cloud.google.com/go/longrunning v0.6.7
	cloud.google.com/go/profiler v0.4.3
	cloud.google.com/go/trace v1.11.6
	contrib.go.opencensus.io/exporter/stackdriver v0.13.14
	github.com/Microsoft/go-winio v0.6.2
	github.com/bazelbuild/reclient/api v0.0.0-20240617160057-89d6134e48e5
	github.com/bazelbuild/remote-apis v0.0.0-20250410133023-536ec595e1df
	github.com/bazelbuild/remote-apis-sdks v0.0.0-20250708204855-85b23cd12656
	github.com/biogo/hts v1.4.5
	github.com/golang/glog v1.2.5
	github.com/google/go-cmp v0.7.0
	github.com/google/uuid v1.6.0
	github.com/klauspost/compress v1.18.0
	github.com/klauspost/cpuid/v2 v2.2.11
	github.com/maruel/subcommands v1.1.1
	github.com/pkg/xattr v0.4.12
	go.chromium.org/build/kajiya v0.0.0-00010101000000-000000000000
	go.chromium.org/luci v0.0.0-20250709063223-f8ca329df43e
	go.opencensus.io v0.24.0
	go.starlark.net v0.0.0-20250701195324-d457b4515e0e
	golang.org/x/oauth2 v0.30.0
	golang.org/x/sync v0.15.0
	golang.org/x/sys v0.33.0
	golang.org/x/term v0.32.0
	google.golang.org/api v0.240.0
	google.golang.org/genproto v0.0.0-20250707201910-8d1bb00bc6a7
	google.golang.org/genproto/googleapis/api v0.0.0-20250707201910-8d1bb00bc6a7
	google.golang.org/genproto/googleapis/bytestream v0.0.0-20250707201910-8d1bb00bc6a7
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250707201910-8d1bb00bc6a7
	google.golang.org/grpc v1.73.0
	google.golang.org/protobuf v1.36.6
)

require (
	cloud.google.com/go v0.121.2 // indirect
	cloud.google.com/go/auth v0.16.2 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.2.8 // indirect
	cloud.google.com/go/monitoring v1.24.2 // indirect
	github.com/GoogleCloudPlatform/protoc-gen-bq-schema v0.0.0-20190119112626-026f9fcdf705 // indirect
	github.com/aws/aws-sdk-go v1.43.31 // indirect
	github.com/census-instrumentation/opencensus-proto v0.4.1 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/mock v1.7.0-rc.1 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/pprof v0.0.0-20250602020802-c6617b811d0e // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.6 // indirect
	github.com/googleapis/gax-go/v2 v2.14.2 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/julienschmidt/httprouter v1.3.0 // indirect
	github.com/prometheus/prometheus v0.35.0 // indirect
	github.com/texttheater/golang-levenshtein v1.0.1 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	go.opentelemetry.io/auto/sdk v1.1.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.61.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.61.0 // indirect
	go.opentelemetry.io/otel v1.36.0 // indirect
	go.opentelemetry.io/otel/metric v1.36.0 // indirect
	go.opentelemetry.io/otel/trace v1.36.0 // indirect
	golang.org/x/crypto v0.39.0 // indirect
	golang.org/x/net v0.41.0 // indirect
	golang.org/x/text v0.26.0 // indirect
	golang.org/x/time v0.12.0 // indirect
)
