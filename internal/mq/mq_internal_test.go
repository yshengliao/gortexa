package mq

import (
	"reflect"
	"testing"

	apperr "github.com/yshengliao/gortexa/internal/errors"
)

func TestSplitBrokers(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		want    []string
		wantErr bool
	}{
		{name: "single broker", url: "127.0.0.1:9092", want: []string{"127.0.0.1:9092"}},
		{name: "multi broker with spaces", url: "b1:9092, b2:9092 ,b3:9092", want: []string{"b1:9092", "b2:9092", "b3:9092"}},
		{name: "empty url", url: "", wantErr: true},
		{name: "trailing comma", url: "b1:9092,", wantErr: true},
		{name: "blank entry", url: "b1:9092, ,b2:9092", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := splitBrokers(c.url)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got %v", got)
				}
				if !apperr.Is(err, apperr.CatInvalidArgument) {
					t.Fatalf("category = %v, want InvalidArgument", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}
