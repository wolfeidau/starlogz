import {
  createConnectQueryKey,
  useMutation,
  useQuery,
} from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import {
  getOperationsOverview,
  getOperationsTelemetry,
  getSession,
  revokeOperationsOAuthGrant,
  revokeOperationsWebSession,
} from "../../../api/gen/proto/es/starlogz/v1/ui-UIService_connectquery";
import { OperationsView } from "./operations";

export function OperationsRoute() {
  const session = useQuery(getSession, {});
  const isOperator = Boolean(session.data?.isOperator);
  const operations = useQuery(
    getOperationsOverview,
    { limit: 50 },
    { enabled: isOperator },
  );
  const telemetry = useQuery(
    getOperationsTelemetry,
    {},
    { enabled: isOperator },
  );
  const queryClient = useQueryClient();
  // Returning the promise makes mutateAsync wait for the refreshed table.
  const onSuccess = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: getOperationsOverview,
        cardinality: "finite",
      }),
    });
  const revokeWebSession = useMutation(revokeOperationsWebSession, {
    onSuccess,
  });
  const revokeOAuthGrant = useMutation(revokeOperationsOAuthGrant, {
    onSuccess,
  });

  if (!isOperator) {
    return (
      <section className="empty-panel">
        Service-operator access is required.
      </section>
    );
  }

  return (
    <OperationsView
      overview={operations.data}
      loading={operations.isLoading}
      error={operations.error}
      telemetry={telemetry.data}
      telemetryLoading={telemetry.isLoading}
      telemetryError={telemetry.error}
      currentWebSessionId={session.data?.webSessionId}
      onRevokeWebSession={async (id) => {
        await revokeWebSession.mutateAsync({ id });
      }}
      onRevokeOAuthGrant={async (id) => {
        await revokeOAuthGrant.mutateAsync({ id });
      }}
    />
  );
}
