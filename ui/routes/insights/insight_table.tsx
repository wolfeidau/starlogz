import type { Insight } from "../../../api/gen/proto/es/starlogz/v1/ui_pb";
import { Pill } from "../../components/pill";
import { Tag } from "../../components/tag";
import { RenderedMarkdown } from "../../lib/markdown";
import { formatTimestamp } from "../../lib/time";
import "./insights.css";

export function InsightTable({
  insights,
  onOpenInsight,
}: {
  insights: Insight[];
  onOpenInsight: (key: string) => void;
}) {
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Insight</th>
            <th>Category</th>
            <th>Source</th>
            <th>Tags</th>
            <th>Updated</th>
          </tr>
        </thead>
        <tbody>
          {insights.map((insight) => (
            <tr key={insight.id}>
              <td>
                <div className="insight-content">
                  <RenderedMarkdown
                    html={insight.renderedHtml}
                    onOpenInsight={onOpenInsight}
                  />
                </div>
                {insight.key && <code>{insight.key}</code>}
              </td>
              <td>
                <Pill>{insight.category}</Pill>
              </td>
              <td>{insight.source}</td>
              <td>
                <div className="tag-list">
                  {insight.tags.map((tag) => (
                    <Tag key={tag}>{tag}</Tag>
                  ))}
                </div>
              </td>
              <td className="nowrap">{formatTimestamp(insight.updatedAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
