# Reply delegate

WriteFreely federates outward only. A blog is a real ActivityPub actor: it
publishes posts, it accepts followers, and remote instances show its articles.
What it cannot do is receive. There is no inbox handling for replies and no
page that could display one, so a reply to a blog post is delivered to the
blog's inbox, stored by nobody, and read by no one. The reader gets no error —
their reply simply goes nowhere.

A **reply delegate** is a fediverse account, on some other instance, that the
blog names in every post it federates. Replies then reach that account, and the
conversation happens somewhere built to hold one.

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
  is federated, so it takes effect on new posts and on edits of old ones.
* **The delegate is resolved by webfinger** the first time it is used, and from
  the database after that. If it cannot be resolved — the handle is wrong, or
  the remote instance is down at that moment — the post federates without the
  mention rather than failing.
* **A blocked or allowlist-excluded delegate gets nothing.** Delivery still
  goes through the federation allowlist, if one is configured.
* **It is not an inbox.** The blog still cannot show replies, reply counts, or
  a comment section. Readers see the conversation on the delegate's instance.
