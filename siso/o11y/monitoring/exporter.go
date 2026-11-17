// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package monitoring

import (
	"context"
	"fmt"
	"slices"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	smetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type staticLabelExporter struct {
	smetric.Exporter
	labels []attribute.KeyValue
}

func newStaticLabelExporter(exporter smetric.Exporter, labels []attribute.KeyValue) *staticLabelExporter {
	return &staticLabelExporter{
		Exporter: exporter,
		labels:   slices.Clone(labels),
	}
}

func (e *staticLabelExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	for i := range rm.ScopeMetrics {
		for j := range rm.ScopeMetrics[i].Metrics {
			m := &rm.ScopeMetrics[i].Metrics[j]
			switch data := m.Data.(type) {
			case metricdata.Gauge[int64]:
				addDataPointLabels(data.DataPoints, e.labels)
				m.Data = data
			case metricdata.Gauge[float64]:
				addDataPointLabels(data.DataPoints, e.labels)
				m.Data = data
			case metricdata.Sum[int64]:
				addDataPointLabels(data.DataPoints, e.labels)
				m.Data = data
			case metricdata.Sum[float64]:
				addDataPointLabels(data.DataPoints, e.labels)
				m.Data = data
			case metricdata.Histogram[int64]:
				addHistogramDataPointLabels(data.DataPoints, e.labels)
				m.Data = data
			case metricdata.Histogram[float64]:
				addHistogramDataPointLabels(data.DataPoints, e.labels)
				m.Data = data
			case metricdata.ExponentialHistogram[int64]:
				addExponentialHistogramDataPointLabels(data.DataPoints, e.labels)
				m.Data = data
			case metricdata.ExponentialHistogram[float64]:
				addExponentialHistogramDataPointLabels(data.DataPoints, e.labels)
				m.Data = data
			case metricdata.Summary:
				addSummaryDataPointLabels(data.DataPoints, e.labels)
				m.Data = data
			default:
				otel.Handle(fmt.Errorf("add static labels to metric %q: unsupported aggregation %T", m.Name, m.Data))
			}
		}
	}
	return e.Exporter.Export(ctx, rm)
}

func addDataPointLabels[N int64 | float64](points []metricdata.DataPoint[N], labels []attribute.KeyValue) {
	for i := range points {
		points[i].Attributes = mergeAttributes(labels, points[i].Attributes)
	}
}

func addHistogramDataPointLabels[N int64 | float64](points []metricdata.HistogramDataPoint[N], labels []attribute.KeyValue) {
	for i := range points {
		points[i].Attributes = mergeAttributes(labels, points[i].Attributes)
	}
}

func addExponentialHistogramDataPointLabels[N int64 | float64](points []metricdata.ExponentialHistogramDataPoint[N], labels []attribute.KeyValue) {
	for i := range points {
		points[i].Attributes = mergeAttributes(labels, points[i].Attributes)
	}
}

func addSummaryDataPointLabels(points []metricdata.SummaryDataPoint, labels []attribute.KeyValue) {
	for i := range points {
		points[i].Attributes = mergeAttributes(labels, points[i].Attributes)
	}
}

func mergeAttributes(labels []attribute.KeyValue, attrs attribute.Set) attribute.Set {
	if len(labels) == 0 {
		return attrs
	}
	pointAttrs := attrs.ToSlice()
	// NewSet sorts its input in place. Keep it separate from labels and put
	// point attributes last so they win duplicate keys.
	merged := make([]attribute.KeyValue, 0, len(labels)+len(pointAttrs))
	merged = append(merged, labels...)
	merged = append(merged, pointAttrs...)
	return attribute.NewSet(merged...)
}
