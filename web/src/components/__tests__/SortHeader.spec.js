import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { defineComponent, ref } from 'vue'

import SortHeader from '@/components/SortHeader.vue'
import SortSelect from '@/components/SortSelect.vue'
import { byAddress, byText, useSort } from '@/lib/sort'

/** A table of two columns. */
const Host = defineComponent({
  components: { SortHeader, SortSelect },
  setup() {
    const rows = ref([
      { ip: '10.0.0.20', name: 'b' },
      { ip: '10.0.0.3', name: 'a' },
    ])
    const sort = useSort(
      rows,
      { ip: byAddress((r) => r.ip), name: byText((r) => r.name) },
      { by: 'ip' },
    )
    return {
      sort,
      columns: [
        ['ip', 'Address'],
        ['name', 'Name'],
      ],
    }
  },
  template: `
    <SortSelect :sort="sort" :columns="columns" />
    <table>
      <thead><tr>
        <SortHeader by="ip" :sort="sort">Address</SortHeader>
        <SortHeader by="name" :sort="sort">Name</SortHeader>
      </tr></thead>
      <tbody><tr v-for="r in sort.sorted.value" :key="r.ip"><td>{{ r.ip }}</td></tr></tbody>
    </table>`,
})

const ips = (w) => w.findAll('td').map((td) => td.text())
const header = (w, name) => w.findAll('th').find((th) => th.text() === name)

describe('SortHeader', () => {
  it('sorts by its column and says which way', async () => {
    const wrapper = mount(Host)
    expect(header(wrapper, 'Address').attributes('aria-sort')).toBe('ascending')
    expect(header(wrapper, 'Name').attributes('aria-sort')).toBeUndefined()
    expect(ips(wrapper)).toEqual(['10.0.0.3', '10.0.0.20'])

    await header(wrapper, 'Address').get('button').trigger('click')
    expect(header(wrapper, 'Address').attributes('aria-sort')).toBe('descending')
    expect(ips(wrapper)).toEqual(['10.0.0.20', '10.0.0.3'])

    await header(wrapper, 'Name').get('button').trigger('click')
    expect(header(wrapper, 'Address').attributes('aria-sort')).toBeUndefined()
    expect(header(wrapper, 'Name').attributes('aria-sort')).toBe('ascending')
  })
})

describe('SortSelect', () => {
  it('says Sort until a list with no order of its own is sorted', async () => {
    const sort = useSort([{ ip: '10.0.0.1' }], { ip: byAddress((r) => r.ip) })
    const wrapper = mount(SortSelect, { props: { sort, columns: [['ip', 'Address']] } })
    expect(wrapper.get('select').element.value).toBe('')
    expect(wrapper.get('option').text()).toBe('Sort')
    await wrapper.get('select').setValue('ip')
    expect(sort.order.value).toEqual({ by: 'ip', dir: 'asc' })
    expect(wrapper.findAll('option').map((o) => o.text())).toEqual(['Address'])
  })

  it('sorts by the column chosen, in its own order', async () => {
    const wrapper = mount(Host)
    const select = wrapper.get('select')
    expect(select.attributes('aria-label')).toBe('Sort')
    expect(select.element.value).toBe('ip')
    await select.setValue('name')
    expect(header(wrapper, 'Name').attributes('aria-sort')).toBe('ascending')
    expect(ips(wrapper)).toEqual(['10.0.0.3', '10.0.0.20'])
  })
})
