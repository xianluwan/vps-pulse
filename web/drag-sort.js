(()=>{
  const read=key=>{try{return JSON.parse(localStorage.getItem(key)||'[]')}catch{return[]}};
  const save=(key,ids)=>localStorage.setItem(key,JSON.stringify(ids));
  const rank=(items,order,id)=>[...items].sort((a,b)=>{const ai=order.indexOf(String(id(a))),bi=order.indexOf(String(id(b)));return(ai<0?1e9:ai)-(bi<0?1e9:bi)});
  const originalRender=render;
  render=function(){data=rank(data,read('vpspulse:vps-order'),v=>v.id);originalRender();attachVPS()};
  function makeSortable(container,selector,key,idOf){
    if(!container)return;
    if(container.querySelector('.dragging'))return;
    const items=[...container.querySelectorAll(selector)];
    const sorted=rank(items,read(key),idOf);
    if(sorted.some((item,index)=>item!==items[index]))sorted.forEach(item=>container.appendChild(item));
    items.forEach(item=>{
      if(item.dataset.sortReady)return;
      item.dataset.sortReady='1';item.draggable=true;item.title='按住拖动可调整顺序';
      item.addEventListener('dragstart',event=>{if(event.target.closest('button,summary,input,select,a')){event.preventDefault();return}item.classList.add('dragging');event.dataTransfer.effectAllowed='move';event.dataTransfer.setData('text/plain',String(idOf(item)))});
      item.addEventListener('dragend',()=>{save(key,[...container.querySelectorAll(selector)].map(idOf));item.classList.remove('dragging')});
      item.addEventListener('dragover',event=>{event.preventDefault();const dragging=container.querySelector(`${selector}.dragging`);if(!dragging||dragging===item)return;const after=event.clientY>item.getBoundingClientRect().top+item.offsetHeight/2;container.insertBefore(dragging,after?item.nextSibling:item)});
    });
  }
  function attachVPS(){makeSortable($('cards'),'.server-row','vpspulse:vps-order',item=>item.querySelector('details[data-vps-id]')?.dataset.vpsId||'')}
  function attachMonitors(){makeSortable($('monitorList'),'.monitor-item','vpspulse:monitor-order',item=>{const code=item.querySelector('button[onclick^="editMonitor"]')?.getAttribute('onclick')||'';return code.match(/\d+/)?.[0]||''})}
  new MutationObserver(()=>{attachVPS();attachMonitors()}).observe(document.body,{childList:true,subtree:true});
  attachVPS();
})();
