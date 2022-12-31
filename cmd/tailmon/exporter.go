package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type exporter struct {
	name     string
	port     int
	hostname string
}

func (e *exporter) TailscaleNodeName() string {
	return fmt.Sprintf("tailmon/%s/%s", e.name, e.hostname)
}

// newExporter takes a name like "node-exporter:9100"
// and saves the name, port, and hostname
func newExporter(value string) (exporter, error) {
	ep := exporter{}

	name, portStr, ok := strings.Cut(value, ":")
	if !ok {
		return ep, errors.New("use name-exporter:port format")
	}

	port, err := strconv.ParseInt(portStr, 10, 64)
	if err != nil {
		return ep, err
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	ep.name = name
	ep.port = int(port)
	ep.hostname = hostname
	return ep, nil
}
