import "./login_view.css";

export function LoginView() {
  return (
    <main className="login-shell">
      <section className="login-panel">
        <div>
          <p className="eyebrow">starlogz</p>
          <h1>Project insights</h1>
          <p className="login-copy">
            Browse facts, decisions, preferences, and context captured by your
            agents.
          </p>
        </div>
        <a className="primary-link" href="/login">
          Sign in with GitHub
        </a>
      </section>
    </main>
  );
}
