package main

import "testing"

func TestSnapchatPage(t *testing.T) {
	for _, u := range []string{"https://web.snapchat.com/", "https://www.snapchat.com/web", "https://www.snapchat.com/web/", "https://snapchat.com/web", "https://www.snapchat.com/add-friends/"} {
		if !snapchatPage(u) {
			t.Errorf("valid Snapchat page rejected: %s", u)
		}
	}
	for _, u := range []string{"https://web.snapchat.com.example.org/", "http://web.snapchat.com/", "https://www.snapchat.com/", "https://www.snapchat.com/web-other", "https://accounts.snapchat.com/"} {
		if snapchatPage(u) {
			t.Errorf("other page accepted: %s", u)
		}
	}
}
