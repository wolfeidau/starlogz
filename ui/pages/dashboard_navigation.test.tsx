import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { useDashboardNavigation } from "./dashboard_navigation";

const projects = [{ slug: "alpha" }, { slug: "beta" }];

beforeEach(() => {
  window.history.replaceState({}, "", "/dashboard");
});

afterEach(cleanup);

describe("dashboard navigation state", () => {
  test("initializes a deep link after projects load", () => {
    window.history.replaceState(
      {},
      "",
      "/dashboard?project=alpha&insight_key=source",
    );
    const { result, rerender } = renderHook(
      ({ available }) => useDashboardNavigation(available),
      { initialProps: { available: [] as typeof projects } },
    );

    expect(result.current.activeProject).toBe("");
    expect(result.current.detailSelector).toBeNull();

    rerender({ available: projects });

    expect(result.current.activeProject).toBe("alpha");
    expect(result.current.detailSelector).toEqual({
      case: "key",
      value: "source",
    });
  });

  test("navigates between projects and closes detail through URL state", () => {
    window.history.replaceState(
      {},
      "",
      "/dashboard?project=alpha&insight_key=source",
    );
    const { result } = renderHook(() => useDashboardNavigation(projects));

    act(() => {
      result.current.navigate("beta", { case: "id", value: "019f-target" });
    });
    expect(result.current.activeProject).toBe("beta");
    expect(result.current.detailSelector).toEqual({
      case: "id",
      value: "019f-target",
    });
    expect(window.location.search).toBe("?project=beta&insight_id=019f-target");

    act(() => result.current.navigate("beta", null));
    expect(result.current.detailSelector).toBeNull();
    expect(window.location.search).toBe("?project=beta");
  });

  test("applies popstate and corrects inaccessible projects", async () => {
    const { result } = renderHook(() => useDashboardNavigation(projects));

    act(() => {
      window.history.pushState(
        {},
        "",
        "/dashboard?project=beta&insight_key=target",
      );
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(result.current.activeProject).toBe("beta");
    expect(result.current.detailSelector).toEqual({
      case: "key",
      value: "target",
    });

    act(() => {
      window.history.pushState(
        {},
        "",
        "/dashboard?project=missing&insight_key=orphaned",
      );
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(result.current.activeProject).toBe("alpha");
    expect(result.current.detailSelector).toBeNull();
    await waitFor(() => {
      expect(window.location.search).toBe("?project=alpha");
    });
  });
});
