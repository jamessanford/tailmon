### Experimental!  Do not use in production!

- Expose prometheus metrics over tailscale.
- Securely poll your metrics from services across the globe through a dedicated metrics tailnet.
- Built-in service discovery.

Point [Prometheus HTTP SD](https://prometheus.io/docs/prometheus/latest/http_sd/) at http://tailmon-discover/
and any new "tailmon" exporters you run will be scraped automatically.

### Get started

1. Run tailmon on nodes that have exporters

  ```
  node1% tailmon -state . node-exporter:9100
  node2% tailmon -state . node-exporter:9100 postgres-exporter:9817
  ```

2. Run a single instance of tailmon-discover

  ```
  prom-node% tailmon-discover -state /var/lib/tailmon
  ```

3. Configure prometheus.yaml

Use HTTP SD to discover which endpoints to monitor:

```
scrape_configs:
  - job_name: 'tailnet'
    scrape_interval: 60s
    http_sd_configs:
    - url: http://tailmon-discover/
```

Optionally add rewrites to set "job" and "node":

```
    relabel_configs:
      - source_labels: [__meta_tailmon_exporter_name]
        target_label: job
      - source_labels: [__meta_tailmon_node_name]
        target_label: node
```

### Overview

`tailmon` registers hostnames like `tailmon/node-exporter/node1`
  on the tailnet and accepts connections on port 80 to /metrics,
  which it proxies to the correct localhost port.

`tailmon-discover` exports the list of `tailmon/*`
  instances in Prometheus HTTP SD format.

If your exporter nodes are not trustworthy, use Tailscale ACLs to prevent outgoing connections.

### Diagram

1. tailmon

  ```
  [node-exporter/node1]:80/metrics on the tailnet
    ->
       tailmon
               ->
                  localhost:9100/metrics
  ```

2. tailmon-discover

  Connects to tailnet, finds `tailmon/*` nodes, exports them
  in [Prometheus HTTP SD](https://prometheus.io/docs/prometheus/latest/http_sd/) format at http://tailmon-discover/

  Example:
  ```
    [
        {
            "targets": [
                "[fd7a:0123:4444::7]:80"
            ],
            "labels": {
                "__meta_tailmon_exporter_name": "node-exporter",
                "__meta_tailmon_node_name": "node1",
                "__meta_tailscale_dns_name": "tailmon-node-exporter-node1.ts.example.com"
            }
        },
        ...
  ```

### Background

I have services all over that I'd like to pull metrics from securely.

I wanted to try tailscale, but I didn't want to expose every
service on every node into the tailnet.  Creating a dedicated
"metrics" tailnet seemed like a neat idea.
