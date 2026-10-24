import {useEffect,useState} from 'react'
import {api,V1} from '../lib/api'
import {Modal,Loading,errMsg} from './ui'
export function TrashDialog({id,episode='',title,onClose,onRemoved}:{id:string;episode?:string;title:string;onClose:()=>void;onRemoved:()=>void}){
 const [revision,setRevision]=useState<number>()
 const [move,setMove]=useState(true),[busy,setBusy]=useState(false),[error,setError]=useState('')
 useEffect(()=>{void api.get<{item:{updated_at:number}}>(`${V1}/catalog/items/${encodeURIComponent(id)}`).then(v=>setRevision(v.item.updated_at)).catch(e=>setError(errMsg(e)))},[id])
 const remove=async()=>{setBusy(true);setError('');try{await api.post(`${V1}/catalog/items/${encodeURIComponent(id)}/trash`,{revision,move_files:move,episode_id:episode});onRemoved()}catch(e){setError(errMsg(e))}finally{setBusy(false)}}
 return <Modal title="Move to trash?" onClose={onClose}><div className="stack"><p>Remove “{title}” from the library? Your listening history is preserved.</p><label><input type="checkbox" checked={move} onChange={e=>setMove(e.target.checked)}/> Move its files into trash too</label><p className="muted">{move?'Files stay on disk until you permanently remove them from Trash. You can restore them there.':'Files stay in their original folders. Restore the catalog entry from Trash to show it again.'}</p>{error&&<p role="alert" className="callout danger">{error} Check Trash if a file move needs attention.</p>}{revision===undefined&&!error&&<Loading/>}<div className="row"><button className="btn" onClick={onClose} disabled={busy}>Cancel</button><button className="btn danger" disabled={busy||revision===undefined} onClick={remove}>Move to trash</button></div></div></Modal>
}
