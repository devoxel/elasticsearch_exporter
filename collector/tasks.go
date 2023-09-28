// Copyright 2023 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/alecthomas/kingpin/v2"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
)

// filterByTask global required because collector interface doesn't expose any way to take
// constructor args.
var actionFilter string

var taskActionDesc = prometheus.NewDesc(
	prometheus.BuildFQName(namespace, "task_stats", "action_total"),
	"Number of tasks of a certain action",
	[]string{"action"}, nil)

func init() {
	kingpin.Flag("tasks.actions",
		"Filter on task actions. Used in same way as Task API actions param").
		Default("indices:*").StringVar(&actionFilter)
	registerCollector("tasks", defaultDisabled, NewTaskCollector)
}

// Task Information Struct
type TaskCollector struct {
	logger log.Logger
	hc     *http.Client
	u      *url.URL
}

// NewTaskCollector defines Task Prometheus metrics
func NewTaskCollector(logger log.Logger, u *url.URL, hc *http.Client) (Collector, error) {
	level.Info(logger).Log("msg", "task collector created",
		"actionFilter", actionFilter,
	)

	return &TaskCollector{
		logger: logger,
		hc:     hc,
		u:      u,
	}, nil
}

func (t *TaskCollector) Update(ctx context.Context, ch chan<- prometheus.Metric) error {
	stats, err := t.fetchAndDecodeAndAggregateTaskStats()
	if err != nil {
		err = fmt.Errorf("failed to fetch and decode task stats: %w", err)
		return err
	}
	for action, count := range stats.CountByAction {
		ch <- prometheus.MustNewConstMetric(
			taskActionDesc,
			prometheus.GaugeValue,
			float64(count),
			action,
		)
	}
	return nil
}

func (t *TaskCollector) fetchAndDecodeAndAggregateTaskStats() (*AggregatedTaskStats, error) {
	u := t.u.ResolveReference(&url.URL{Path: "_tasks"})
	q := u.Query()
	q.Set("group_by", "none")
	q.Set("actions", actionFilter)
	u.RawQuery = q.Encode()

	res, err := t.hc.Get(u.String())
	if err != nil {
		return nil, fmt.Errorf("failed to get data stream stats health from %s://%s:%s%s: %s",
			u.Scheme, u.Hostname(), u.Port(), u.Path, err)
	}

	defer func() {
		err = res.Body.Close()
		if err != nil {
			level.Warn(t.logger).Log(
				"msg", "failed to close http.Client",
				"err", err,
			)
		}
	}()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP Request to %v failed with code %d", u.String(), res.StatusCode)
	}

	bts, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var tr TasksResponse
	if err := json.Unmarshal(bts, &tr); err != nil {
		return nil, err
	}

	stats := AggregateTasks(tr)
	return stats, nil
}

// TasksResponse is a representation of the Task management API.
type TasksResponse struct {
	Tasks []TaskResponse `json:"tasks"`
}

// TaskResponse is a representation of the individual task item returned by task API endpoint.
//
// We only parse a very limited amount of this API for use in aggregation.
type TaskResponse struct {
	Action string `json:"action"`
}

type AggregatedTaskStats struct {
	CountByAction map[string]int64
}

func AggregateTasks(t TasksResponse) *AggregatedTaskStats {
	actions := map[string]int64{}
	for _, task := range t.Tasks {
		actions[task.Action] += 1
	}
	agg := &AggregatedTaskStats{CountByAction: actions}
	return agg
}
