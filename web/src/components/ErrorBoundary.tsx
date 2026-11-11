import { Component, type ErrorInfo, type ReactNode } from "react";

// A page that throws while rendering used to take the whole portal down to a
// blank screen, with nothing to say what happened or how to get back. This
// catches it where the page is drawn: the menu stays, the error is named, and
// moving to another page clears it.

type Props = { children: ReactNode; resetKey: string };
type State = { error: Error | null };

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("page failed", error, info.componentStack);
  }

  componentDidUpdate(prev: Props) {
    // Another page is another chance.
    if (prev.resetKey !== this.props.resetKey && this.state.error) {
      this.setState({ error: null });
    }
  }

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;
    return (
      <div className="page">
        <div className="card page-error">
          <div className="card-title">This page ran into a problem</div>
          <p className="muted">
            The rest of the portal still works — pick another page in the menu, or reload this one. If it keeps
            happening, send the text below to whoever runs the portal.
          </p>
          <pre className="page-error-text">{error.message || String(error)}</pre>
          <div className="row" style={{ gap: 8 }}>
            <button className="btn" onClick={() => window.location.reload()}>
              Reload the page
            </button>
            <button className="btn ghost" onClick={() => this.setState({ error: null })}>
              Try again
            </button>
          </div>
        </div>
      </div>
    );
  }
}
