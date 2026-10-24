import { Link } from "@tanstack/react-router";

export function NotFound() {
  return (
    <section className="empty-panel">
      <p>Page not found.</p>
      <Link to="/dashboard">Back to dashboard</Link>
    </section>
  );
}
