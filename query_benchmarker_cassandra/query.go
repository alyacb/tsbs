package main

import (
	"fmt"
	"time"
)

// HLQuery is a high-level query, usually read from stdin after being
// generated by a bulk query generator program.
//
// The primary use of an HLQuery is to combine it with a ClientSideIndex to
// construct a QueryPlan.
type HLQuery struct {
	HumanLabel       []byte
	HumanDescription []byte
	ID               int64

	MeasurementName []byte // e.g. "cpu"
	FieldName       []byte // e.g. "usage_user"
	AggregationType []byte // e.g. "avg" or "sum". used literally in the cassandra query.
	TimeStart       time.Time
	TimeEnd         time.Time
	GroupByDuration time.Duration
	TagSets         [][]string // semantically, each subgroup is OR'ed and they are all AND'ed together
}

// String produces a debug-ready description of a Query.
func (q *HLQuery) String() string {
	return fmt.Sprintf("ID: %d, HumanLabel: %s, HumanDescription: %s, MeasurementName: %s, FieldName: %s, AggregationType: %s, TimeStart: %s, TimeEnd: %s, GroupByDuration: %s, TagSets: %s", q.ID, q.HumanLabel, q.HumanDescription, q.MeasurementName, q.FieldName, q.AggregationType, q.TimeStart, q.TimeEnd, q.GroupByDuration, q.TagSets)
}

// ForceUTC rewrites timestamps to be in UTC, which is helpful for
// pretty-printing.
func (q *HLQuery) ForceUTC() {
	q.TimeStart = q.TimeStart.UTC()
	q.TimeEnd = q.TimeEnd.UTC()
}

// ToQueryPlan combines an HLQuery with a ClientSideIndex to make a QueryPlan.
func (q *HLQuery) ToQueryPlan(csi *ClientSideIndex) (qp *QueryPlan, err error) {
	seriesChoices := csi.CopyOfSeriesCollection()

	// Build the time buckets used for 'group by time'-type queries.
	//
	// It is important to populate these even if they end up being empty,
	// so that we get correct results for empty 'time buckets'.
	tis := bucketTimeIntervals(q.TimeStart, q.TimeEnd, q.GroupByDuration)
	bucketedSeries := map[TimeInterval][]Series{}
	for _, ti := range tis {
		bucketedSeries[ti] = []Series{}
	}

	// For each known db series, associate it to its applicable time
	// buckets, if any:
	for _, s := range seriesChoices {
		// quick skip if the series doesn't match at all:
		if !s.MatchesMeasurementName(string(q.MeasurementName)) {
			continue
		}
		if !s.MatchesFieldName(string(q.FieldName)) {
			continue
		}
		if !s.MatchesTagSets(q.TagSets) {
			continue
		}

		// check each group-by interval to see if it applies:
		for _, ti := range tis {
			if !s.MatchesTimeInterval(&ti) {
				continue
			}
			bucketedSeries[ti] = append(bucketedSeries[ti], s)
		}
	}

	// For each group-by time bucket, convert its series into CQLQueries:
	cqlBuckets := make(map[TimeInterval][]CQLQuery, len(bucketedSeries))
	for ti, seriesSlice := range bucketedSeries {
		cqlQueries := make([]CQLQuery, len(seriesSlice))
		for i, ser := range seriesSlice {
			start := ti.Start
			end := ti.End

			// the following two special cases ensure equivalency with rounded time boundaries as seen in influxdb:
			// https://docs.influxdata.com/influxdb/v0.13/query_language/data_exploration/#rounded-group-by-time-boundaries
			if start.Before(q.TimeStart) {
				start = q.TimeStart
			}
			if end.After(q.TimeEnd) {
				end = q.TimeEnd
			}

			cqlQueries[i] = NewCQLQuery(string(q.AggregationType), ser.Table, ser.Id, start.UnixNano(), end.UnixNano())
		}
		cqlBuckets[ti] = cqlQueries
	}

	qp, err = NewQueryPlan(string(q.AggregationType), cqlBuckets)
	return
}

// Type CQLQuery wraps data needed to execute a gocql.Query.
type CQLQuery struct {
	PreparableQueryString string
	Args                  []interface{}
}

// NewCQLQuery builds a CQLQuery, using prepared CQL statements.
func NewCQLQuery(aggrLabel, tableName, rowName string, timeStartNanos, timeEndNanos int64) CQLQuery {
	preparableQueryString := fmt.Sprintf("SELECT %s(value) FROM %s WHERE series_id = ? AND timestamp_ns >= ? AND timestamp_ns < ?", aggrLabel, tableName)
	args := []interface{}{rowName, timeStartNanos, timeEndNanos}
	return CQLQuery{preparableQueryString, args}
}

// Type CQLResult holds a result from a set of CQL aggregation queries.
// Used for debug printing.
type CQLResult struct {
	TimeInterval
	Value float64
}
