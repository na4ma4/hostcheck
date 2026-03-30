module github.com/na4ma4/hostcheck/plugins/dns

go 1.26.1

require (
	github.com/dosquad/go-cliversion v0.3.0
	github.com/miekg/dns v1.1.72
	github.com/na4ma4/go-hostcheck-interface v0.0.0-20260330015257-2c1a0c04e9de
	google.golang.org/protobuf v1.36.11
)

require (
	golang.org/x/mod v0.34.0 // indirect
	golang.org/x/net v0.52.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/tools v0.43.0 // indirect
)

replace github.com/na4ma4/hostcheck => ../..
