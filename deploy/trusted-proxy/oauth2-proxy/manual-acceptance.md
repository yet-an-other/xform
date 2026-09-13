# ZITADEL manual acceptance checklist

Run this checklist against a disposable real ZITADEL tenant and a test Panel.
Use a test Operator address in the exact-email file. Do not paste client
secrets, cookies, tokens, or `id_token_hint` values into this document or
issue/CI logs.

## ZITADEL application

- [ ] The application is **Web**, not Native, User Agent, API, implicit,
      device, or client-credentials.
- [ ] Grant is **Authorization Code**.
- [ ] Client authentication is **BASIC** (`client_secret_basic`).
- [ ] PKCE arrives from the proxy, not from ZITADEL: the rendered config
      sets `code_challenge_method = "S256"`, and no ZITADEL PKCE setting is
      required — the console's PKCE preset is a public client without a
      secret and is not used.
- [ ] OIDC issuer discovery is enabled; issuer/TLS verification is not
      disabled.
- [ ] Requested scopes are exactly `openid email`.
- [ ] The returned ID token contains the expected email and
      `email_verified=true` for the test Operator.
- [ ] The callback registered in ZITADEL exactly equals the selected rendered
      config's `redirect_url` (scheme, host, path, and no trailing slash).
- [ ] ZITADEL development mode is off for the production-like test.

## Exact-email authorization

- [ ] The test Operator's verified address, and only that exact address, is in
      `authenticated-emails.txt`.
- [ ] A verified address not in the file is denied, including one at the same
      domain.
- [ ] An unverified address is denied even if its text is added to the file.
- [ ] The config contains no `email_domains`, wildcard, or role allowlist.
- [ ] Any separately tested role policy uses a flat string-array claim and is
      not mistaken for ZITADEL's native role map.

## Root mount

- [ ] Render `root.cfg` with the real HTTPS origin and run the matching
      gateway.
- [ ] Visiting `/` while unauthenticated redirects to the same-origin
      `/oauth2/start` flow, then to ZITADEL.
- [ ] Successful callback is exactly `/oauth2/callback`; the Panel loads after
      admission.
- [ ] `__Host-xform_oauth2` has `Secure`, `HttpOnly`, `SameSite=Lax`, no
      `Domain`, and `Path=/`.
- [ ] `/oauth2/sign_out` clears the oauth2-proxy cookie and returns to the
      Panel. The surviving ZITADEL browser session may sign in again.
- [ ] With a short-lived test cookie configuration, expiry returns document
      requests to sign-in and API requests to `401`; no stale admission is
      accepted.

## `/xform/` subpath mount

- [ ] Render `subpath.cfg` with the same real HTTPS origin and run the
      `/xform/` gateway shape.
- [ ] `/xform` redirects to `/xform/`; no route outside the mount admits a
      request.
- [ ] Successful callback is exactly `/xform/oauth2/callback`.
- [ ] `__Secure-xform_oauth2` has `Secure`, `HttpOnly`, `SameSite=Lax`, and
      `Path=/xform/`; it is not sent to an unrelated path.
- [ ] `/xform/oauth2/sign_out` performs local Panel sign-out.
- [ ] Repeat the denial and expiry checks under the subpath.

## Boundary and logout

- [ ] Send spoofed assertion, identity, Basic, `Authorization`, access-token,
      and ID-token headers from the browser. The gateway replaces/removes them;
      xform receives only its configured Admission assertion after a successful
      auth check.
- [ ] Stop oauth2-proxy or make its auth check fail. The public gateway fails
      closed; it does not inject an assertion or offer Password authentication.
- [ ] An unauthenticated `/api/*` request remains a `401`, not an HTML sign-in
      redirect.
- [ ] The default sign-out ends only the oauth2-proxy browser session. Confirm
      that this is documented to Operators.
- [ ] If RP-initiated ZITADEL end-session logout is enabled separately, verify
      the exact post-logout redirect and URL encoding, and warn testers that
      ZITADEL may end every ZITADEL session represented by the current browser
      cookie. Do not treat it as the default or as a Panel-only logout.
