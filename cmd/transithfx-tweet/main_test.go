package main

import (
	"testing"
	"time"

	"golang.org/x/text/message"
)

func TestAltText(t *testing.T) {
	weeksAgo := func(n int) time.Time {
		return time.Now().AddDate(0, 0, -7*n)
	}

	cases := []struct {
		name string
		data []week
		want string
	}{
		{
			name: "fewer than",
			data: []week{
				{start: weeksAgo(2), end: weeksAgo(1), count: 5000},
				{start: weeksAgo(1), end: weeksAgo(0), count: 4500},
			},
			want: "Bar chart of passengers by week for last 2 weeks. The most recent count of 4,500 is 10% fewer than the previous week.",
		},
		{
			name: "more than",
			data: []week{
				{start: weeksAgo(2), end: weeksAgo(1), count: 5000},
				{start: weeksAgo(1), end: weeksAgo(0), count: 5500},
			},
			want: "Bar chart of passengers by week for last 2 weeks. The most recent count of 5,500 is 10% more than the previous week.",
		},
		{
			name: "about the same",
			data: []week{
				{start: weeksAgo(2), end: weeksAgo(1), count: 5000},
				{start: weeksAgo(1), end: weeksAgo(0), count: 5001},
			},
			want: "Bar chart of passengers by week for last 2 weeks. The most recent count of 5,001 is about the same as the previous week.",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := message.NewPrinter(message.MatchLanguage("en"))
			got := altText(c.data, p)
			if got != c.want {
				t.Errorf("got alt text\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}
