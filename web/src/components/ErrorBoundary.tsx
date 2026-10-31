import { Component, type ErrorInfo, type ReactNode } from "react";

import { t } from "@/lib/i18n";
import { href } from "@/lib/router";

// ErrorBoundary keeps a render error in one screen from blanking the whole
// page: it shows what broke and a retry instead. resetKey (the route) clears
// the error when the user goes elsewhere.
export class ErrorBoundary extends Component<
  { resetKey: string; children: ReactNode },
  { error: Error | null; key: string }
> {
  state: { error: Error | null; key: string } = { error: null, key: this.props.resetKey };

  static getDerivedStateFromProps(props: { resetKey: string }, state: { error: Error | null; key: string }) {
    if (props.resetKey !== state.key) return { error: null, key: props.resetKey };
    return null;
  }

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("srvmon: screen crashed", error, info.componentStack);
  }

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;
    return (
      <div className="flex min-h-screen flex-col items-start gap-3 p-6">
        <p className="text-crit">{t("The screen failed to render.")}</p>
        <pre className="max-w-full overflow-auto text-xs text-muted-foreground">{String(error.message || error)}</pre>
        <div className="flex gap-4">
          <button type="button" className="text-primary" onClick={() => this.setState({ error: null })}>
            [{t("retry")}]
          </button>
          <a href={href.overview()} className="text-primary">
            ← {t("to the overview")}
          </a>
        </div>
      </div>
    );
  }
}
