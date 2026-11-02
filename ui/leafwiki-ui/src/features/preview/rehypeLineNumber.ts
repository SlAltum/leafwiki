import { Element } from 'hast'
import { Plugin } from 'unified'
import { visit } from 'unist-util-visit'

import { slugifyHeadline } from '@/lib/headingAnchor'

// Re-exported so every existing importer keeps its import path.
export { slugifyHeadline }

function getNodeText(node: Element): string {
  let text = ''

  visit(node, (child) => {
    if (child.type === 'text') {
      text += child.value
    }
  })

  return text
}

export const rehypeLineNumber: Plugin = () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return (tree: any) => {
    const slugCounts: Record<string, number> = {}

    visit(tree, 'element', (node: Element) => {
      const line = node.position?.start?.line
      node.properties = node.properties || {}

      if (line && !('data-line' in node.properties)) {
        node.properties['data-line'] = String(line)
      }

      if (!/^h[1-6]$/.test(node.tagName) || 'id' in node.properties) {
        return
      }

      const baseSlug = slugifyHeadline(getNodeText(node))
      if (!baseSlug) return

      const duplicateCount = slugCounts[baseSlug] ?? 0
      slugCounts[baseSlug] = duplicateCount + 1

      node.properties.id =
        duplicateCount === 0 ? baseSlug : `${baseSlug}-${duplicateCount}`
      node.properties['data-leafwiki-generated-id'] = 'true'
    })
  }
}
