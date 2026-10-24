import "./login_view.css";

export function LoginView() {
  return (
    <main className="login-shell">
      <section className="login-panel">
        <p className="login-wordmark">
          <span className="wordmark-star" aria-hidden="true">
            ✦
          </span>
          starlogz
        </p>
        <p className="login-copy">
          The project memory your agents keep. Sign in to read it.
        </p>
        <a className="primary-link" href="/login">
          Sign in with GitHub
        </a>
      </section>
    </main>
  );
}
