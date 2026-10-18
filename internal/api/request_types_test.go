package api

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/magarcia/expensify-cli/internal/config"
)

func TestReadRetainsRequestTypeAcrossSessionRenewal(t *testing.T) {
	params := url.Values{"policyID": {"ABC"}, "apiRequestType": {"write"}}
	want := params.Encode()
	reads, auths := 0, 0
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.URL.Path == "/api/Authenticate" {
			auths++
			io.WriteString(w, `{"jsonCode":200,"authToken":"renewed"}`)
			return
		}
		reads++
		if r.URL.Path != "/api/GetPolicyCategories" || r.Form.Get("apiRequestType") != "read" || r.Form.Get("policyID") != "ABC" {
			t.Fatalf("incorrect member read: %s, type=%q", r.URL.Path, r.Form.Get("apiRequestType"))
		}
		if reads == 1 {
			io.WriteString(w, `{"jsonCode":407}`)
			return
		}
		if r.Form.Get("authToken") != "renewed" {
			t.Fatal("read did not use renewed session")
		}
		io.WriteString(w, `{"jsonCode":200}`)
	}, config.Credentials{AuthToken: "expired", PartnerUserID: "device", PartnerUserSecret: "secret"}, nil)
	if _, err := client.Read(context.Background(), "GetPolicyCategories", params); err != nil {
		t.Fatal(err)
	}
	if reads != 2 || auths != 1 || params.Encode() != want {
		t.Fatalf("reads=%d auths=%d caller params changed=%v", reads, auths, params.Encode() != want)
	}
}
