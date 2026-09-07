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
	"strings"
	"testing"

	"github.com/writeas/web-core/activitystreams"
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

// applyReplyDelegate runs inside ActivityObject, which is called from post
// rendering as well as federation — including in tests and tools that build an
// App with no datastore. Resolution needs the database, so the absence of one
// must be a no-op rather than a panic.
func TestApplyReplyDelegateWithoutADatastore(t *testing.T) {
	app, p := apVisPost(CollPublic, apVisArticleBody)
	p.Collection.ReplyDelegate = delegateHandle

	o := p.ActivityObject(app)

	if countMentions(o, delegateIRI) != 0 {
		t.Errorf("no delegate should be resolvable without a datastore, got tag=%+v", o.Tag)
	}
}

// A blog with no delegate configured must federate exactly as it did before
// the setting existed.
func TestActivityObjectWithoutAReplyDelegateIsUnchanged(t *testing.T) {
	app, p := apVisPost(CollPublic, apVisArticleBody)

	o := p.ActivityObject(app)

	if len(o.Tag) != 0 {
		t.Errorf("a post with no delegate and no tags must carry no Mention, got tag=%+v", o.Tag)
	}
	if len(o.CC) != 1 {
		t.Errorf("a public post's cc must hold only the followers collection, got cc=%v", o.CC)
	}
}

// The consent gate's state machine. Handle, resolution and the follow are
// three independent facts, and the page's wording depends on which one is
// missing, so each has to map to its own status rather than collapse into
// "not working".
func TestReplyDelegateStatusOf(t *testing.T) {
	for _, tc := range []struct {
		name     string
		handle   string
		actorIRI string
		follows  bool
		want     replyDelegateStatus
	}{
		{"no delegate configured", "", "", false, replyDelegateUnset},
		{"no delegate configured, stray follow", "", delegateIRI, true, replyDelegateUnset},
		{"configured but never resolved", delegateHandle, "", false, replyDelegateNotFollowing},
		{"unresolved outranks a stale follow flag", delegateHandle, "", true, replyDelegateNotFollowing},
		{"resolved but not following", delegateHandle, delegateIRI, false, replyDelegateNotFollowing},
		{"resolved and following", delegateHandle, delegateIRI, true, replyDelegateFollowing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := replyDelegateStatusOf(tc.handle, tc.actorIRI, tc.follows); got != tc.want {
				t.Errorf("replyDelegateStatusOf(%q, %q, %v) = %q, want %q", tc.handle, tc.actorIRI, tc.follows, got, tc.want)
			}
		})
	}
}

// A follow that has not happened is the one status the owner has to act on, so
// that message must name both accounts: the one to sign in to and the one to
// follow. Everything else about the feature is invisible from the outside.
func TestReplyDelegateStatusMessageNamesBothAccounts(t *testing.T) {
	const blogHandle = "@quigs@blog.example"

	msg := replyDelegateStatusMessage(replyDelegateNotFollowing, delegateHandle, blogHandle)
	if !strings.Contains(msg, delegateHandle) {
		t.Errorf("the not-following message must name the delegate, got %q", msg)
	}
	if !strings.Contains(msg, blogHandle) {
		t.Errorf("the not-following message must name the blog to follow, got %q", msg)
	}

	if got := replyDelegateStatusMessage(replyDelegateUnset, "", blogHandle); got != "" {
		t.Errorf("an unset delegate has nothing to report, got %q", got)
	}
	for _, status := range []replyDelegateStatus{replyDelegateFollowing, replyDelegateUnreachable} {
		if got := replyDelegateStatusMessage(status, delegateHandle, blogHandle); !strings.Contains(got, delegateHandle) {
			t.Errorf("the %q message must name the delegate, got %q", status, got)
		}
	}
}

// The handle the owner is told to follow has to be the blog's actual fediverse
// handle. hostName is a full URL, so a naive concatenation would tell them to
// follow "@quigs@https://blog.example".
func TestCollectionFediverseHandle(t *testing.T) {
	c := &Collection{Alias: "quigs", hostName: "https://blog.example"}
	if got, want := collectionFediverseHandle(c), "@quigs@blog.example"; got != want {
		t.Errorf("collectionFediverseHandle() = %q, want %q", got, want)
	}
	if got := collectionFediverseHandle(nil); got != "" {
		t.Errorf("collectionFediverseHandle(nil) = %q, want empty", got)
	}
}

// replyDelegateState runs from a page render, so it must survive the same
// no-datastore App the rest of this file exercises rather than panic on it.
func TestReplyDelegateStateWithoutADatastore(t *testing.T) {
	app := &App{}

	if status, _ := replyDelegateState(app, &Collection{Alias: "quigs"}, false); status != replyDelegateUnset {
		t.Errorf("a blog with no delegate should report %q, got %q", replyDelegateUnset, status)
	}
	if status, _ := replyDelegateState(app, &Collection{Alias: "quigs", ReplyDelegate: delegateHandle}, false); status != replyDelegateNotFollowing {
		t.Errorf("a delegate with nothing cached should report %q, got %q", replyDelegateNotFollowing, status)
	}
	if status, _ := replyDelegateState(nil, nil, true); status != replyDelegateUnset {
		t.Errorf("no app and no collection should report %q, got %q", replyDelegateUnset, status)
	}
}

// The consent gate, at the level this file can reach without a datastore: an
// actor nobody can show a follow for is not mentioned. replyDelegateFollows-
// Collection is what applyReplyDelegate consults, and its answer for an
// unknown actor has to be "no" rather than an error the caller might ignore.
func TestReplyDelegateFollowsCollectionIsFalseWithoutADatastore(t *testing.T) {
	follows, err := replyDelegateFollowsCollection(&App{}, &Collection{ID: 1, Alias: "quigs"}, delegateIRI)
	if err != nil {
		t.Fatalf("checking a follow without a datastore should not error, got %v", err)
	}
	if follows {
		t.Error("a follow that cannot be read must not be reported as a follow")
	}
}

// A delegate that cannot be resolved when the owner asks for a lookup is a
// different report to one that simply has nothing cached: the first names a
// wrong handle or an unreachable instance, the second is the ordinary state
// of a page render. Only the resolving path may say "unreachable".
func TestReplyDelegateStateSeparatesUnreachableFromUncached(t *testing.T) {
	c := &Collection{Alias: "quigs", ReplyDelegate: delegateHandle}

	if status, _ := replyDelegateState(&App{}, c, true); status != replyDelegateUnreachable {
		t.Errorf("a lookup that came back empty should report %q, got %q", replyDelegateUnreachable, status)
	}
	if status, _ := replyDelegateState(&App{}, c, false); status != replyDelegateNotFollowing {
		t.Errorf("a page render with nothing cached should report %q, got %q", replyDelegateNotFollowing, status)
	}
}

// warmReplyDelegate runs on every save of every blog, including saves that
// touch nothing to do with the delegate, so each way of carrying "no delegate
// here" has to be a no-op rather than a panic or a stray lookup.
func TestWarmReplyDelegateIgnoresNothingToDo(t *testing.T) {
	empty := ""
	junk := "not a handle"
	valid := delegateHandle
	for _, tc := range []struct {
		name      string
		submitted *string
	}{
		{"field absent from the form", nil},
		{"delegate cleared", &empty},
		{"not a full handle", &junk},
		{"valid handle but no datastore", &valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			warmReplyDelegate(&App{}, tc.submitted)
			warmReplyDelegate(nil, tc.submitted)
		})
	}
}
