/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package writefreely

import (
	"testing"
	"time"

	"github.com/guregu/null"
	"github.com/guregu/null/zero"
	"github.com/writeas/web-core/activitystreams"
	"github.com/writefreely/writefreely/config"
)

func TestNormalizeReplyDelegate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    string
		want  string
		valid bool
	}{
		{"empty means no delegate", "", "", true},
		{"whitespace only means no delegate", "   ", "", true},
		{"full handle is kept", "@joe@talk.example", "@joe@talk.example", true},
		{"leading @ is added", "joe@talk.example", "@joe@talk.example", true},
		{"surrounding space is trimmed", "  @joe@talk.example  ", "@joe@talk.example", true},
		{"dotted local part is allowed", "@joe.q@talk.example", "@joe.q@talk.example", true},
		{"subdomain host is allowed", "@joe@social.talk.example", "@joe@social.talk.example", true},
		{"local-only handle is rejected", "@joe", "", false},
		{"bare host is rejected", "talk.example", "", false},
		{"hostless handle is rejected", "@joe@talk", "", false},
		{"a URL is rejected", "https://talk.example/@joe", "", false},
		{"trailing junk is rejected", "@joe@talk.example and more", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeReplyDelegate(tc.in)
			if ok != tc.valid {
				t.Fatalf("normalizeReplyDelegate(%q) validity = %v, want %v", tc.in, ok, tc.valid)
			}
			if got != tc.want {
				t.Errorf("normalizeReplyDelegate(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

const (
	delegateHandle = "@joe@talk.example"
	delegateIRI    = "https://talk.example/u/joe"
)

func countMentions(o *activitystreams.Object, iri string) int {
	n := 0
	for _, t := range o.Tag {
		if t.Type == "Mention" && t.HRef == iri {
			n++
		}
	}
	return n
}

func countCC(o *activitystreams.Object, iri string) int {
	n := 0
	for _, cc := range o.CC {
		if cc == iri {
			n++
		}
	}
	return n
}

// The delegate has to land in BOTH fields. `cc` is what delivers the activity
// to the account; `tag` is what receivers build their mention records from,
// and a receiver's mention record is what pre-fills a reply's addressing.
// Either one alone loses half the feature.
func TestAddReplyDelegateMentionAddressesAndTags(t *testing.T) {
	o := activitystreams.NewArticleObject()
	o.CC = []string{"https://quigs.blog/api/collections/quigs/followers"}

	if !addReplyDelegateMention(o, delegateHandle, delegateIRI) {
		t.Fatal("adding a delegate to an object that lacks one should report a change")
	}
	if countCC(o, delegateIRI) != 1 {
		t.Errorf("delegate must be addressed exactly once in cc, got cc=%v", o.CC)
	}
	if countMentions(o, delegateIRI) != 1 {
		t.Errorf("delegate must be tagged as a Mention exactly once, got tag=%+v", o.Tag)
	}
	for _, tag := range o.Tag {
		if tag.HRef == delegateIRI && tag.Name != delegateHandle {
			t.Errorf("Mention name = %q, want the full handle %q", tag.Name, delegateHandle)
		}
	}
}

// An author who writes the delegate's handle into the post must not get the
// delegate twice. federatePost delivers one copy of the activity per Mention
// tag, so a duplicate tag is a duplicate delivery, not just untidy JSON.
func TestAddReplyDelegateMentionDoesNotDuplicateAnExistingMention(t *testing.T) {
	o := activitystreams.NewArticleObject()
	o.CC = []string{delegateIRI}
	o.Tag = []activitystreams.Tag{{Type: "Mention", HRef: delegateIRI, Name: delegateHandle}}

	if addReplyDelegateMention(o, delegateHandle, delegateIRI) {
		t.Error("adding a delegate that is already mentioned should report no change")
	}
	if countCC(o, delegateIRI) != 1 {
		t.Errorf("delegate must not be addressed twice, got cc=%v", o.CC)
	}
	if countMentions(o, delegateIRI) != 1 {
		t.Errorf("delegate must not be tagged twice, got tag=%+v", o.Tag)
	}
}

// Addressed but untagged is the case worth covering separately: delivery
// already happens, but without the tag no receiver records a mention, so no
// reply is ever pre-filled with the delegate. Tag it, and do not re-address.
func TestAddReplyDelegateMentionTagsAnAlreadyAddressedDelegate(t *testing.T) {
	o := activitystreams.NewArticleObject()
	o.CC = []string{delegateIRI}

	if !addReplyDelegateMention(o, delegateHandle, delegateIRI) {
		t.Fatal("tagging an addressed-but-untagged delegate should report a change")
	}
	if countCC(o, delegateIRI) != 1 {
		t.Errorf("an already-addressed delegate must not be added to cc again, got cc=%v", o.CC)
	}
	if countMentions(o, delegateIRI) != 1 {
		t.Errorf("delegate must be tagged exactly once, got tag=%+v", o.Tag)
	}
}

func TestAddReplyDelegateMentionIgnoresIncompleteInput(t *testing.T) {
	for _, tc := range []struct {
		name             string
		handle, actorIRI string
	}{
		{"no handle", "", delegateIRI},
		{"unresolved actor", delegateHandle, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := activitystreams.NewArticleObject()
			if addReplyDelegateMention(o, tc.handle, tc.actorIRI) {
				t.Error("an incomplete delegate should report no change")
			}
			if len(o.CC) != 0 || len(o.Tag) != 0 {
				t.Errorf("an incomplete delegate must not touch addressing, got cc=%v tag=%+v", o.CC, o.Tag)
			}
		})
	}
}

// replyDelegatePost builds the smallest PublicPost that ActivityObject will
// serialise. The blank line in the content makes it an Article.
func replyDelegatePost() (*App, *PublicPost) {
	cfg := config.New()
	cfg.App.Host = "https://blog.example"

	coll := Collection{Alias: "you", Title: "you"}
	coll.hostName = cfg.App.Host

	p := &PublicPost{
		Post: &Post{
			ID:      "abc123",
			Slug:    null.NewString("delegate-test", true),
			Title:   zero.NewString("Delegate test", true),
			Content: "Intro line.\n\nSecond paragraph, so this serialises as an Article.\n",
			Created: time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
		},
		Collection: &CollectionObj{Collection: coll},
	}
	return &App{cfg: cfg}, p
}

// applyReplyDelegate runs inside ActivityObject, which is called from post
// rendering as well as federation — including in tests and tools that build an
// App with no datastore. Resolution needs the database, so the absence of one
// must be a no-op rather than a panic.
func TestApplyReplyDelegateWithoutADatastore(t *testing.T) {
	app, p := replyDelegatePost()
	p.Collection.ReplyDelegate = delegateHandle

	o := p.ActivityObject(app)

	if countMentions(o, delegateIRI) != 0 {
		t.Errorf("no delegate should be resolvable without a datastore, got tag=%+v", o.Tag)
	}
}

// A blog with no delegate configured must federate exactly as it did before
// the setting existed.
func TestActivityObjectWithoutAReplyDelegateIsUnchanged(t *testing.T) {
	app, p := replyDelegatePost()

	o := p.ActivityObject(app)

	if len(o.Tag) != 0 {
		t.Errorf("a post with no delegate and no tags must carry no Mention, got tag=%+v", o.Tag)
	}
	if len(o.CC) != 1 {
		t.Errorf("cc must hold only the followers collection, got cc=%v", o.CC)
	}
}
