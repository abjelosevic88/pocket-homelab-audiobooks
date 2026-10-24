export interface User { id: number; email: string; name: string; role: 'admin' | 'member'; created_at: string }
export interface APIToken { id: number; name: string; prefix: string; last_used_at: string | null; created_at: string }

export interface Ref { id: string; name: string; sequence?: string }
export interface Chapter { id: number; start: number; end: number; title: string }

/** An audiobook in Audiobookshelf, with the person's progress. */
export interface Book {
  id: string; library_id: string; title: string; subtitle?: string; author: string; narrator?: string; series?: string; sequence?: string
  authors?: Ref[]; series_list?: Ref[]; genres: string[]; year?: string; duration: number; cover?: string; added_at: number; chapters: number; missing?: boolean
  progress: number; current_time: number; finished: boolean; last_played?: number; finished_at?: number
  description?: string; publisher?: string; language?: string; explicit?: boolean; abridged?: boolean; narrators?: string[]
  chapter_list?: Chapter[]; files?: number; size?: number; format?: string
}
export interface Bookmark { libraryItemId: string; title: string; time: number; createdAt: number }
export interface BookPage {
  book: Book; bookmarks: Bookmark[]; series: { id: string; name: string; sequence: string; books: Book[] }[]; up_next: boolean; progress_id?: string; started_at?: number
  listened: { seconds: number; sessions: number; first: string }; open_url: string
}
export interface SeriesCard { id: string; library_id: string; name: string; books: number; covers: string[]; finished: number; next?: Book; added_at: number }
export interface AuthorCard { id: string; library_id?: string; name: string; books: number; image?: string }
export interface Library { id: string; name: string; icon: string; count: number; hidden: boolean }

export interface Home {
  continue: Book[]; series_next: Book[]; up_next: Book[]; recent: Book[]; discover: Book[]; listen_again: Book[]; authors: AuthorCard[]; libraries: Library[]
  today: { seconds: number; week_seconds: number; goal_minutes: number; streak: number }
}

export interface SyncState { watermark: number; backfilled: boolean; synced_at: string; error: string }

export interface Listen {
  id: number; abs_id: string; item_id: string; library_id: string; title: string; author: string; narrator: string; series: string; genres: string[]
  duration: number; day: string; started_at: string; updated_at: string; seconds: number; start_pos: number; end_pos: number; device: string; source: string; cover: string
}
export interface Finish { id: number; item_id: string; title: string; author: string; duration: number; finished_at: string; day: string; cover: string }
export interface CountRow { key: string; name: string; count: number; seconds: number; image?: string }
export interface Stats {
  range: string; from: string; to: string; days_active: number; average_seconds: number
  totals: { sessions: number; seconds: number; books: number }
  series: { key: string; label: string; seconds: number }[]; top_books: CountRow[]; top_authors: CountRow[]; top_narrators: CountRow[]; top_genres: CountRow[]; devices: CountRow[]
  weekday: number[]; hours: number[]; finished: number; finished_books: Finish[]; goal_minutes: number; streak: { longest: number; current: number }
}
export interface DayBook { item_id: string; title: string; author: string; cover: string; seconds: number; from: number; to: number; first: string; last: string; devices: string; finished: boolean }
export interface Day { date: string; seconds: number; books: DayBook[]; finished: { item_id: string; title: string; author: string; cover: string; finished_at: string }[]; sessions: number; summary: string }

export interface PlayTrack { url: string; start: number; duration: number; mime: string }
export interface PlayInfo { session_id: string; method: 'direct' | 'hls'; tracks: PlayTrack[]; current_time: number; duration: number; chapters: Chapter[]; book: Book; speed: number }
export interface NowPlaying {
  active: boolean; playing?: boolean; item_id?: string; title?: string; author?: string; cover?: string; position?: number; duration?: number
  device?: string; app?: string; updated_at?: number; server_time: number
}

export interface Connection {
  mode: 'core' | 'key' | 'direct' | 'off'; connected: boolean; message?: string; via?: 'key' | 'direct' | 'core'; account?: string; admin?: boolean
  server_version?: string; public_url?: string; username?: string; custom_url?: string; server_url: boolean; password_login?: boolean
  core: boolean; core_configured: boolean; libraries?: Library[]; in_progress?: number; sync: SyncState; syncing: boolean
}

export interface Settings {
  core_configured: boolean; core_registered?: boolean; title: string; date_format: string; time_format: string
  speed: number; skip_back: number; skip_forward: number; rewind_on_resume: number; play_next: boolean; hidden_libraries: string; daily_goal: number
  notify_url: string; notify_format: string; notify_finished: boolean; notify_weekly: boolean; notify_configured: boolean
  setup_complete: boolean; base_url: string; abs_url: boolean; abs_public_url: string
}
export interface UpNextEntry { id: number; item_id: string; title: string; author: string; position: number; added_at: string }
