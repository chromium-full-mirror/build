// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package monitoring

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	smetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type countingExporter struct {
	exports int
	points  []exportedPoint
}

type exportedPoint struct {
	metricName string
	attributes attribute.Set
}

func (*countingExporter) Temporality(smetric.InstrumentKind) metricdata.Temporality {
	return metricdata.CumulativeTemporality
}

func (*countingExporter) Aggregation(smetric.InstrumentKind) smetric.Aggregation {
	return smetric.AggregationDefault{}
}

func (e *countingExporter) Export(_ context.Context, rm *metricdata.ResourceMetrics) error {
	e.exports++
	for i := range rm.ScopeMetrics {
		for j := range rm.ScopeMetrics[i].Metrics {
			m := &rm.ScopeMetrics[i].Metrics[j]
			for _, attrs := range aggregationAttributes(m.Data) {
				e.points = append(e.points, exportedPoint{metricName: m.Name, attributes: attrs})
			}
		}
	}
	return nil
}

func (*countingExporter) ForceFlush(context.Context) error { return nil }

func (*countingExporter) Shutdown(context.Context) error { return nil }

func TestNewMetricProviderRequiresConfig(t *testing.T) {
	_, err := NewMetricProvider(t.Context(), "metrics-project", "rbe-project", &countingExporter{}, nil)
	if err == nil {
		t.Fatal("NewMetricProvider() succeeded with a nil config")
	}
}

func TestNewMetricProviderAddsStaticLabels(t *testing.T) {
	labels := []attribute.KeyValue{
		attribute.String(versionKey, "static-version"),
		attribute.String(osFamilyKey, "linux"),
	}
	config := &MetricConfig{staticLabels: labels}

	delegate := &countingExporter{}
	mp, err := NewMetricProvider(t.Context(), "metrics-project", "rbe-project", delegate, config)
	if err != nil {
		t.Fatalf("NewMetricProvider() failed: %v", err)
	}
	shutdown := sync.OnceFunc(func() {
		if err := mp.Shutdown(t.Context()); err != nil {
			t.Errorf("MeterProvider.Shutdown() failed: %v", err)
		}
	})
	defer shutdown()
	meter := mp.Meter("test")
	counter, err := meter.Int64Counter("test.counter")
	if err != nil {
		t.Fatalf("Int64Counter() failed: %v", err)
	}
	counter.Add(t.Context(), 1, metric.WithAttributes(
		attribute.String(versionKey, "point-version"),
		attribute.String("point", "value"),
	))
	_, err = meter.Int64ObservableGauge(
		"test.gauge",
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(1, metric.WithAttributes(attribute.String("async", "value")))
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Int64ObservableGauge() failed: %v", err)
	}
	for range 2 {
		if err := mp.ForceFlush(t.Context()); err != nil {
			t.Fatalf("MeterProvider.ForceFlush() failed: %v", err)
		}
	}
	shutdown()

	seen := make(map[string]bool)
	for _, point := range delegate.points {
		seen[point.metricName] = true
		var wantAttrs attribute.Set
		switch point.metricName {
		case "test.counter":
			wantAttrs = attribute.NewSet(
				attribute.String(versionKey, "point-version"),
				attribute.String(osFamilyKey, "linux"),
				attribute.String("point", "value"),
			)
		case "test.gauge":
			wantAttrs = attribute.NewSet(
				attribute.String(versionKey, "static-version"),
				attribute.String(osFamilyKey, "linux"),
				attribute.String("async", "value"),
			)
		default:
			t.Errorf("unexpected exported metric %q", point.metricName)
			continue
		}
		if !point.attributes.Equals(&wantAttrs) {
			t.Errorf("%s attributes = %v, want %v", point.metricName, point.attributes.ToSlice(), wantAttrs.ToSlice())
		}
	}
	for _, name := range []string{"test.counter", "test.gauge"} {
		if !seen[name] {
			t.Errorf("metric %q was not exported", name)
		}
	}
}

func TestStaticLabelExporter(t *testing.T) {
	pointAttrs := attribute.NewSet(
		attribute.String(versionKey, "point-version"),
		attribute.String("grpc.method", "/google.bytestream.ByteStream/Read"),
	)
	exemplarAttrs := []attribute.KeyValue{attribute.String("filtered", "value")}
	rm := metricdata.ResourceMetrics{
		ScopeMetrics: []metricdata.ScopeMetrics{{
			Metrics: []metricdata.Metrics{
				{Name: "gauge-int", Data: metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{{Attributes: pointAttrs, Exemplars: []metricdata.Exemplar[int64]{{FilteredAttributes: exemplarAttrs}}}}}},
				{Name: "gauge-float", Data: metricdata.Gauge[float64]{DataPoints: []metricdata.DataPoint[float64]{{Attributes: pointAttrs}}}},
				{Name: "sum-int", Data: metricdata.Sum[int64]{DataPoints: []metricdata.DataPoint[int64]{{Attributes: pointAttrs}}}},
				{Name: "sum-float", Data: metricdata.Sum[float64]{DataPoints: []metricdata.DataPoint[float64]{{Attributes: pointAttrs}}}},
				{Name: "histogram-int", Data: metricdata.Histogram[int64]{DataPoints: []metricdata.HistogramDataPoint[int64]{{Attributes: pointAttrs}}}},
				{Name: "histogram-float", Data: metricdata.Histogram[float64]{DataPoints: []metricdata.HistogramDataPoint[float64]{{Attributes: pointAttrs}}}},
				{Name: "exponential-histogram-int", Data: metricdata.ExponentialHistogram[int64]{DataPoints: []metricdata.ExponentialHistogramDataPoint[int64]{{Attributes: pointAttrs}}}},
				{Name: "exponential-histogram-float", Data: metricdata.ExponentialHistogram[float64]{DataPoints: []metricdata.ExponentialHistogramDataPoint[float64]{{Attributes: pointAttrs}}}},
				{Name: "summary", Data: metricdata.Summary{DataPoints: []metricdata.SummaryDataPoint{{Attributes: pointAttrs}}}},
			},
		}},
	}

	labels := []attribute.KeyValue{
		attribute.String(versionKey, "static-version"),
		attribute.String(osFamilyKey, "linux"),
		attribute.String("custom_label", "custom-value"),
	}
	delegate := &countingExporter{}
	exporter := newStaticLabelExporter(delegate, labels)
	// Keep spare capacity so an in-place sort of append(exporter.labels, ...)
	// would mutate the shared static labels.
	exporter.labels = append(make([]attribute.KeyValue, 0, 16), exporter.labels...)
	labelsBefore := slices.Clone(exporter.labels)

	for range 2 {
		if err := exporter.Export(t.Context(), &rm); err != nil {
			t.Fatalf("Export() failed: %v", err)
		}
	}
	if got, want := delegate.exports, 2; got != want {
		t.Errorf("delegate exports = %d, want %d", got, want)
	}
	if !reflect.DeepEqual(labelsBefore, exporter.labels) {
		t.Errorf("static labels changed after two exports: got %v, want %v", exporter.labels, labelsBefore)
	}

	wantAttrs := attribute.NewSet(
		attribute.String(versionKey, "point-version"),
		attribute.String(osFamilyKey, "linux"),
		attribute.String("custom_label", "custom-value"),
		attribute.String("grpc.method", "/google.bytestream.ByteStream/Read"),
	)
	for _, m := range rm.ScopeMetrics[0].Metrics {
		for _, got := range aggregationAttributes(m.Data) {
			if !got.Equals(&wantAttrs) {
				t.Errorf("%s attributes = %v, want %v", m.Name, got.ToSlice(), wantAttrs.ToSlice())
			}
		}
	}

	gotExemplarAttrs := rm.ScopeMetrics[0].Metrics[0].Data.(metricdata.Gauge[int64]).DataPoints[0].Exemplars[0].FilteredAttributes
	if !reflect.DeepEqual(exemplarAttrs, gotExemplarAttrs) {
		t.Errorf("exemplar filtered attributes = %v, want %v", gotExemplarAttrs, exemplarAttrs)
	}
}

func TestStaticLabelExporterReportsUnknownAggregation(t *testing.T) {
	oldHandler := otel.GetErrorHandler()
	t.Cleanup(func() { otel.SetErrorHandler(oldHandler) })
	var gotErr error
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { gotErr = err }))

	rm := metricdata.ResourceMetrics{
		ScopeMetrics: []metricdata.ScopeMetrics{{
			Metrics: []metricdata.Metrics{{Name: "unknown", Data: nil}},
		}},
	}
	delegate := &countingExporter{}
	exporter := newStaticLabelExporter(delegate, nil)
	if err := exporter.Export(t.Context(), &rm); err != nil {
		t.Fatalf("Export() failed: %v", err)
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), `metric "unknown"`) {
		t.Errorf("OpenTelemetry error = %v, want unsupported aggregation error for metric %q", gotErr, "unknown")
	}
	if got, want := delegate.exports, 1; got != want {
		t.Errorf("delegate exports = %d, want %d", got, want)
	}
}

func aggregationAttributes(data metricdata.Aggregation) []attribute.Set {
	switch data := data.(type) {
	case metricdata.Gauge[int64]:
		return dataPointAttributes(data.DataPoints)
	case metricdata.Gauge[float64]:
		return dataPointAttributes(data.DataPoints)
	case metricdata.Sum[int64]:
		return dataPointAttributes(data.DataPoints)
	case metricdata.Sum[float64]:
		return dataPointAttributes(data.DataPoints)
	case metricdata.Histogram[int64]:
		return histogramDataPointAttributes(data.DataPoints)
	case metricdata.Histogram[float64]:
		return histogramDataPointAttributes(data.DataPoints)
	case metricdata.ExponentialHistogram[int64]:
		return exponentialHistogramDataPointAttributes(data.DataPoints)
	case metricdata.ExponentialHistogram[float64]:
		return exponentialHistogramDataPointAttributes(data.DataPoints)
	case metricdata.Summary:
		attrs := make([]attribute.Set, len(data.DataPoints))
		for i := range data.DataPoints {
			attrs[i] = data.DataPoints[i].Attributes
		}
		return attrs
	default:
		return nil
	}
}

func dataPointAttributes[N int64 | float64](points []metricdata.DataPoint[N]) []attribute.Set {
	attrs := make([]attribute.Set, len(points))
	for i := range points {
		attrs[i] = points[i].Attributes
	}
	return attrs
}

func histogramDataPointAttributes[N int64 | float64](points []metricdata.HistogramDataPoint[N]) []attribute.Set {
	attrs := make([]attribute.Set, len(points))
	for i := range points {
		attrs[i] = points[i].Attributes
	}
	return attrs
}

func exponentialHistogramDataPointAttributes[N int64 | float64](points []metricdata.ExponentialHistogramDataPoint[N]) []attribute.Set {
	attrs := make([]attribute.Set, len(points))
	for i := range points {
		attrs[i] = points[i].Attributes
	}
	return attrs
}
