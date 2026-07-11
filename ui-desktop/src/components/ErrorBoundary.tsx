import { Component, type ErrorInfo, type ReactNode } from "react";

interface ErrorBoundaryProps {
  children: ReactNode;
  /** Rendered in place of the children once an error has been caught. */
  fallback: ReactNode;
  /** Called once, with the error message and a stack excerpt, on catch. */
  onError?: (message: string, stack: string) => void;
}

interface ErrorBoundaryState {
  crashed: boolean;
}

/**
 * Catches render and lifecycle errors from the app tree so a component fault
 * shows a recoverable fallback instead of React unmounting everything into a
 * blank screen. This is the codebase's one class component — error boundaries
 * have no hook equivalent. On catch it reports through `onError` (wired to the
 * local crash recorder) and swaps in `fallback`.
 */
export class ErrorBoundary extends Component<
  ErrorBoundaryProps,
  ErrorBoundaryState
> {
  state: ErrorBoundaryState = { crashed: false };

  static getDerivedStateFromError(): ErrorBoundaryState {
    return { crashed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    const message = error?.message ?? String(error);
    const stack = `${error?.stack ?? ""}\n${info?.componentStack ?? ""}`.trim();
    this.props.onError?.(message, stack);
  }

  render() {
    return this.state.crashed ? this.props.fallback : this.props.children;
  }
}
