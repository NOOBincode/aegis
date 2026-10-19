module github.com/aegis-dev/aegis/mcp-servers/events

go 1.26.0

require (
	github.com/aegis-dev/aegis v0.0.0-00010101000000-000000000000
	github.com/mark3labs/mcp-go v1.1.1
	k8s.io/api v0.37.1
	k8s.io/client-go v0.37.1
)

replace github.com/aegis-dev/aegis => ../../
