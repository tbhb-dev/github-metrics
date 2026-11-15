// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

// Package telemetry sets up metric export.
package telemetry

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/sdk/resource"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Setup exports cumulative metrics every 60 seconds. Without an endpoint it
// returns a no-op meter. Shutdown exports the final observations before closing.
//
//nolint:ireturn // Setup returns the standard meter interface.
func Setup(ctx context.Context, serviceName, version string) (func(context.Context) error, metric.Meter, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, noop.NewMeterProvider().Meter(serviceName), nil
	}

	exporter, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithTemporalitySelector(sdkmetric.CumulativeTemporalitySelector),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create metric exporter: %w", err)
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(time.Minute))),
		sdkmetric.WithResource(resource.NewSchemaless(
			attribute.String("service.name", serviceName),
			attribute.String("service.version", version),
		)),
	)

	return provider.Shutdown, provider.Meter(serviceName), nil
}
