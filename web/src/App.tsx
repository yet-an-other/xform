import { useState } from "react";

import { Dashboard } from "@/pages/dashboard";
import { Login } from "@/pages/login";

function App() {
  // Optimistically show the dashboard; its first poll flips to login on 401.
  const [authenticated, setAuthenticated] = useState(true);

  return authenticated ? (
    <Dashboard onUnauthenticated={() => setAuthenticated(false)} />
  ) : (
    <Login onLogin={() => setAuthenticated(true)} />
  );
}

export default App;
