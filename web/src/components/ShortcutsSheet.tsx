import { Modal } from './ui'

const GROUPS: { title: string; keys: [string[], string][] }[] = [
  { title: 'Go to', keys: [[['h'], 'Home'], [['l'], 'Books'], [['s'], 'Series'], [['a'], 'Authors'], [['n'], 'Up next'], [['y'], 'History'], [['t'], 'Stats'], [[','], 'Settings']] },
  { title: 'Find', keys: [[['/'], 'Search & commands'], [['⌘', 'K'], 'Search & commands'], [['['], 'Show or hide the sidebar'], [['?'], 'This sheet']] },
  { title: 'Listening', keys: [[['space'], 'Play / pause'], [['k'], 'Play / pause'], [['←', '→'], 'Back / forward (Settings → Playback)'], [['⇧', '←'], 'Previous chapter'], [['⇧', '→'], 'Next chapter'], [['<', '>'], 'Slower / faster'], [['b'], 'Bookmark this moment'], [['p'], 'Open or close the player'], [['esc'], 'Close the player']] },
]

export function ShortcutsSheet({ onClose }: { onClose: () => void }) {
  return (
    <Modal title="Keyboard shortcuts" onClose={onClose} size="lg">
      <div className="shortcuts">
        {GROUPS.map(g => (
          <section key={g.title}>
            <h4 className="sc-h">{g.title}</h4>
            {g.keys.map(([keys, label]) => (
              <div key={label + keys.join('')} className="sc-row"><span>{label}</span><span className="sc-keys">{keys.map(k => <kbd key={k}>{k}</kbd>)}</span></div>
            ))}
          </section>
        ))}
      </div>
      <p className="muted small mt">Media keys, headphone buttons and the lock screen work too (previous / next = chapters). In search, <kbd>⇧</kbd><kbd>↵</kbd> plays a book straight away.</p>
    </Modal>
  )
}
