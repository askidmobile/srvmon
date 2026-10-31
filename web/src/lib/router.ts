import { useEffect, useState } from "react";

// Minimal hash router: #/ overview, #/add[/<id>] add wizard,
// #/term[/<id>] terminal.
export function useHashPath(): string {
  const [path, setPath] = useState(() => location.hash.replace(/^#/, "") || "/");
  useEffect(() => {
    const on = () => setPath(location.hash.replace(/^#/, "") || "/");
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return path;
}

export function navigate(path: string) {
  location.hash = path;
}
