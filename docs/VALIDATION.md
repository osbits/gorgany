# Validation

## What a client receives

A DTO that fails validation produces a `422` in the standard envelope:

```json
{
  "status": 422,
  "status_code": "VALIDATION",
  "body": null,
  "errors": [
    {
      "field": "email",
      "err": "email must be a valid email address",
      "rule": "email",
      "path": "email"
    },
    {
      "field": "postal_code",
      "err": "postal_code must be exactly 4",
      "rule": "len",
      "param": "4",
      "path": "address.postal_code"
    }
  ]
}
```

| Key | Meaning |
|-----|---------|
| `field` | The name the client used: the field's `json` tag, else its `scheme` tag, else the Go field name |
| `err` | A human-readable message, localised when the app supplies translations |
| `rule` | The validation tag that failed — key your own copy off this rather than parsing `err` |
| `param` | The rule's parameter: the `4` in `len=4`. Absent for a rule that takes none |
| `path` | The dotted wire path, for a nested DTO. Equal to `field` at the top level |

`rule`, `param` and `path` are `omitempty`, so a top-level `required` failure carries just
`field` and `err`.

### A browser gets a redirect instead

The `422` above is what an **API client** receives — one that sends a JSON `Accept` or
`Content-Type`, or whose path is under `/api/`, or that is in the `api` namespace. Anything
else is treated as a server-rendered form and gets `303 See Other` back to the `Referer`,
with the errors flashed into the session for the view to render.

Two details worth knowing:

- The status is `303`, not `301`. A `301` is permanently cacheable and browsers rewrite it to
  a `GET`, so a browser could cache "POST this URL → GET that one" indefinitely. `303` is the
  post-redirect-get status.
- With **no** `Referer` there is nowhere to go back to, so the `422` is returned instead of a
  redirect to nothing.

Before v2.0 every caller got the `301`, API clients included, which is why the payload above
was unobservable through the framework's own handler.

### Which tag names the field

`json` is checked first, then `scheme`, then the Go name. A JSON body binds through
`json`; the query-string and multipart parsers bind through `scheme`. A DTO that only
ever arrives one way carries only the one tag, so both are consulted.

A field tagged `json:"-"` has no wire name. It can still carry `validate` rules, and if
one fails the Go name is reported — the only honest answer available.

## Localising the messages

Messages are looked up under `validation.<rule>` in the app's translation files, falling
back to the framework's English catalog. Add the keys you want to override:

```yaml
# resource/i18n/de.yaml
validation:
  required: "{:field} ist erforderlich"
  email: "{:field} muss eine gültige E-Mail-Adresse sein"
  min: "{:field} muss mindestens {:param} sein"
```

Resolution order, first non-empty wins:

1. the app's translation for `validation.<rule>` in the request's locale
2. the app's translation for `validation.default`
3. the framework's English message for that rule
4. the framework's English catch-all, `{:field} failed the {:rule} rule`

So a partial catalog is fine: translate `required` and `email`, and every other rule
still produces a readable English message rather than a blank one. Supplying only
`validation.default` covers every rule at once.

### Placeholders

| Placeholder | Value |
|-------------|-------|
| `{:field}` | The field's wire name |
| `{:rule}` | The rule that failed |
| `{:param}` | The rule's parameter |
| `{:value}` | The rejected value, truncated to 64 characters |

`{:value}` is available but no default message uses it: echoing a rejected value back is
how a password or a token ends up in a log or an error response. Use it deliberately, on
a specific rule, or not at all.

### The locale a message is rendered in

The HTTP input resolver uses the request's `{lang}` path parameter, falling back to
`i18n.lang.default` — the same resolution the view renderer uses. Elsewhere,
`ValidateStruct` uses the default locale; call `ValidateStructForLocale` to choose.

```go
// core.IValidator
ValidateStruct(s any) error

// core.ILocalizedValidator — optional, implemented by the built-in validator
ValidateStructForLocale(s any, locale string) error
```

A validator that does not implement `ILocalizedValidator` still works; the resolver falls
back to `ValidateStruct`.

## Rules

Every `go-playground/validator` rule is available. The framework adds:

| Rule | Applies to | Meaning |
|------|-----------|---------|
| `mime` | `model.File` | The upload's MIME type must match |
| `maxSize` | `model.File` | The upload must not exceed the given size |
| `unique` | any | The value must not already exist |
| `lsCompletelyRequired` | `model.LocalizedString` | Every language must be non-empty |
| `mapStringStringCompletelyRequired` | `map[string]string` | Every key must be non-empty |

`validator.DefaultMessages()` returns the built-in English catalog, so you can see what
you are overriding.

An app-registered rule with no message of its own gets the catch-all, which names the
rule:

```go
v.RegisterValidation("neverpasses", func(fl validator.FieldLevel) bool { return false })
// → "token failed the neverpasses rule"
```

Add `validation.neverpasses` to your translations to replace it.

## Embedded structs and shadowing

A DTO field that shadows a field promoted from an embedded struct is validated by its
*own* rules; the promoted copy is excluded. In Go the promoted field is unreachable, so
validating it would report a failure the client has no way to fix.

```go
type Base struct {
    Email string `json:"email" validate:"required,email"`
}

type Dto struct {
    Base
    Email string `json:"email" validate:"required"`   // this one's rules apply
}
```

Two fields of the *same* struct sharing one wire name is a different thing, and it is
now an error rather than a silent surprise:

```go
type Broken struct {
    Primary   string `json:"email"`
    Secondary string `json:"email"`
}
// validator: model.Broken has two fields on the wire name "email" (Primary and
// Secondary); the body parser can only bind one of them, so give one a distinct
// json/scheme tag
```

The parser can bind only one of them, so the other silently stayed zero and validation
reported a name matching neither.

## What changed in v2.0

`field` used to be the Go struct field name and `err` used to be go-playground's raw
sentence:

```json
{
  "field": "MobilePhone",
  "err": "Key: 'CreateUserDto.MobilePhone' Error:Field validation for 'MobilePhone' failed on the 'required' tag"
}
```

No UI could display that, and no client could map `MobilePhone` to the `mobile_phone` it
had sent — so every serious consumer replaced `core.IValidator` wholesale just to rename
fields and translate messages.

The struct's `Field` and `Err` keys are unchanged in name and position; `rule`, `param`
and `path` are new and `omitempty`. See `MIGRATION_v2.md`.
