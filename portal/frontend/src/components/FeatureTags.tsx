import { Tag } from '@carbon/react'
import { Feature } from '../api/client'

// Renders one Tag per flavor feature; the description shows as a native tooltip.
export default function FeatureTags({ features }: { features: Feature[] }) {
  if (!features || features.length === 0) return null
  return (
    <div style={{ margin: '0.75rem 0', display: 'flex', flexWrap: 'wrap', gap: '0.25rem' }}>
      {features.map((f) => (
        <span key={f.key} title={f.description}>
          <Tag type="purple">{f.label}</Tag>
        </span>
      ))}
    </div>
  )
}
