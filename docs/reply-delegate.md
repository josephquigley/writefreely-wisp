# Reply delegate

WriteFreely federates outward only. A blog is a real ActivityPub actor: it
publishes posts, it accepts followers, and remote instances show its articles.
What it cannot do is receive. There is no inbox handling for replies and no
page that could display one, so a reply to a blog post is delivered to the
blog's inbox, stored by nobody, and read by no one. The reader gets no error —
their reply simply goes nowhere.

A **reply delegate** is a fediverse account, on some other instance, that the
blog names in every post it federates — provided that account follows the blog.
Replies then reach that account, and the conversation happens somewhere built
to hold one.

## Setting it

On the blog's **Customize** page, under **Reply Delegate**, enter a full
fediverse handle including the instance:

```
@you@social.example
```

Leave it empty for no delegate, which is how blogs behaved before the setting
existed. The setting is per blog, because a delegate belongs to an author and
one instance hosts many authors. It appears only when federation is enabled.

The account can live on any ActivityPub server that holds conversations —
Mastodon, GoToSocial, Mbin, and so on. It does not have to be on this instance,
and normally should not be.

## The delegate has to follow the blog

Naming an account is not enough. **The delegate is mentioned only while it
follows the blog's actor.** Until it does, posts federate exactly as a blog
with no delegate does: nothing is added to `cc` or `tag`, and the delegate's
instance is never contacted.

The reason is consent. Addressing is delivery: every handle in `cc` and `tag`
gets a copy of the activity pushed to its inbox, and its own instance renders
that as a mention. A setting that accepted any handle its owner typed would let
one blog mail a stranger on every post it ever published, forever, with no way
for the stranger to make it stop short of blocking the blog. Requiring a follow
uses the one signal ActivityPub already carries that means "I want this blog's
posts", and the account can withdraw it at any time by unfollowing — the
mentions then stop on their own, without the blog's owner being involved.

In the ordinary case the blog's owner also owns the delegate account: sign in
to it, follow the blog once, and it is done.

The Customize page reports where the delegate stands, and a **Check** button
re-tests it on demand:

| What it says | What it means |
|--------------|---------------|
| *follows this blog* | Resolved and following. New posts carry the mention. |
| *does not follow this blog yet* | Resolved, but no follow. Posts go out without the mention. |
| *has not been looked up yet* | The handle has never been resolved on this instance. Choose **Check**. |

The page itself only reports what the instance already knows, because a
settings page must not block on another server. **Check** is what permits the
webfinger lookup, and it tests the **saved** delegate, so save any change to
the box before using it. The follow itself is always read locally: whether an
actor follows this blog is a fact this instance already holds, in
`remotefollows`.

## What it does on the wire

The delegate is added to the federated post's `cc` and to its `tag` array as a
`Mention`:

```json
{
  "type": "Article",
  "cc": ["https://blog.example/api/collections/you/followers",
         "https://social.example/users/you"],
  "tag": [{"type": "Mention",
           "href": "https://social.example/users/you",
           "name": "@you@social.example"}]
}
```

Nothing is added to the post's content. Receivers build their mention records
from the `tag` array on ingest rather than by scraping the content, so the
delegate is notified and the handle does not appear at the foot of every
article — on the blog or on the fediverse.

## Why that is enough to route replies

ActivityPub has no mechanism for redirecting inbound activity to a different
actor. An activity is delivered to the inboxes of the actors named in `to`,
`cc` and the `tag` mentions, and nowhere else. Addressing is the only lever,
and this is it.

Every major implementation — Mastodon, GoToSocial, Mbin — pre-fills a reply's
addressing from the parent post's author plus the parent's mentions. Because
the delegate is one of the parent's mentions, a reply to a blog post is
addressed to the delegate by the replying instance, through entirely ordinary
means. The blog needs no inbox and the replier needs to do nothing special.

The thread then lives on the delegate's instance. Follow-up replies address the
delegate directly and never involve the blog again.

## Limits worth knowing

* **Existing posts are not re-addressed.** The mention is applied when a post
  is federated, so it takes effect on new posts and on edits of old ones. The
  same is true of the follow: posts published before the delegate followed the
  blog stay unaddressed until they are edited.
* **The delegate is resolved by webfinger** the first time it is used, and from
  the database after that. If it cannot be resolved — the handle is wrong, or
  the remote instance is down at that moment — the post federates without the
  mention rather than failing.
* **A blocked or allowlist-excluded delegate gets nothing.** Delivery still
  goes through the federation allowlist, if one is configured.
* **It is not an inbox.** The blog still cannot show replies, reply counts, or
  a comment section. Readers see the conversation on the delegate's instance.
