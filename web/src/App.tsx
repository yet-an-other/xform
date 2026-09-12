import { useCallback, useState } from "react";

import { UnauthenticatedError } from "@/lib/api";
import { Dashboard } from "@/pages/dashboard";
import { Login } from "@/pages/login";

const trustedRedirectKey = "xform_trusted_auth_redirect";

function App() {
  // The gateway normally admits the first Dashboard request. Password mode
  // falls back to the local form; trusted mode returns to the gateway and
  // never falls back to a password.
  const [authenticated, setAuthenticated] = useState(true);
  const [trustedFailure, setTrustedFailure] = useState(false);

  const onAuthenticated = useCallback(() => {
    try {
      sessionStorage.removeItem(trustedRedirectKey);
    } catch {
      // Storage may be unavailable in a privacy-restricted browser.
    }
  }, []);

  const onUnauthenticated = useCallback((error?: UnauthenticatedError) => {
    // A gateway-generated 401 may omit the mode. Only an explicit Password
    // response may take the local Password flow; unknown 401s return to the
    // trusted boundary rather than exposing a password form.
    if (error && error.authenticationMode !== "password") {
      try {
        if (sessionStorage.getItem(trustedRedirectKey) === "1") {
          setTrustedFailure(true);
          return;
        }
        sessionStorage.setItem(trustedRedirectKey, "1");
        window.location.reload();
      } catch {
        setTrustedFailure(true);
      }
      return;
    }
    setAuthenticated(false);
  }, []);

  if (trustedFailure) {
    return (
      <main role="alert" className="mx-auto mt-16 w-[min(560px,calc(100%-40px))] text-center">
        The Authentication gateway did not admit this Panel request. Check its configuration.
      </main>
    );
  }
  return authenticated ? (
    <Dashboard onAuthenticated={onAuthenticated} onUnauthenticated={onUnauthenticated} />
  ) : (
    <Login onLogin={() => setAuthenticated(true)} />
  );
}

export default App;
